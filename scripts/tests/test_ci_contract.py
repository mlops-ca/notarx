import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import selectors
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('ci_contract', ROOT / 'scripts/ci-contract.py')
ci = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ci)


class SupervisionTests(unittest.TestCase):
    """Process-group supervision of lane steps: timeout, cancellation, descendants."""
    def setUp(self):
        self.work = tempfile.TemporaryDirectory()
        self.root = Path(self.work.name)
        self.addCleanup(self.work.cleanup)

    def test_owned_command_success_failure_and_timeout(self):
        for body, expected, timeout in [('print("owned command")', 'success', 5),
                                        ('raise SystemExit(7)', 'failure', 5),
                                        ('import time; time.sleep(30)', 'timed_out', 0.05)]:
            with self.subTest(expected=expected):
                result = ci.run_command([sys.executable, '-c', body], self.root / 'child.log', dict(os.environ), timeout)
                self.assertEqual(result['status'], expected)

    def test_actual_cancellation_waits_for_its_owned_child(self):
        source = str(ROOT / 'scripts/ci-contract.py')
        code = ('import importlib.util,json,os,sys; from pathlib import Path; '
                f's=importlib.util.spec_from_file_location("c",{source!r}); '
                'c=importlib.util.module_from_spec(s); s.loader.exec_module(c); '
                f'r=c.run_command([sys.executable,"-c","import time; print(12345,flush=True); time.sleep(30)"],Path({str(self.root / "cancel.log")!r}),dict(os.environ)); '
                'print(json.dumps(r),flush=True)')
        proc = subprocess.Popen([sys.executable, '-B', '-c', code], stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, start_new_session=True)
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(proc.stdout, selectors.EVENT_READ)
                self.assertTrue(selector.select(10), 'owned child did not become ready')
                self.assertEqual(proc.stdout.readline().strip(), b'12345')
            proc.send_signal(signal.SIGTERM)
            output = proc.communicate(timeout=10)[0]
            self.assertEqual(proc.returncode, 0)
            result = json.loads(output)
            self.assertEqual(result['status'], 'cancelled')
            self.assertNotEqual(result['exitCode'], 0)
        finally:
            if proc.poll() is None:
                proc.terminate()
                proc.communicate(timeout=50)

    def test_deadline_and_resistant_descendants_survive_neither_pipe_shape(self):
        for mode in ('leader-eof', 'descendant-eof', 'descendant-inherit'):
            for trigger in ('timed_out', 'cancelled'):
                with self.subTest(mode=mode, trigger=trigger):
                    name = mode + '-' + trigger
                    ready = self.root / (name + '.json')
                    if mode == 'leader-eof':
                        body = ('import json,os,pathlib,time; '
                                f'pathlib.Path({str(ready)!r}).write_text(json.dumps({{"pid":os.getpid(),"pgid":os.getpgrp()}})); '
                                'os.close(1); os.close(2); time.sleep(30)')
                    else:
                        child = ('import json,os,pathlib,signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); '
                                 f'pathlib.Path({str(ready)!r}).write_text(json.dumps({{"pid":os.getpid(),"pgid":os.getpgrp()}})); time.sleep(30)')
                        redirect = ',stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL' if mode == 'descendant-eof' else ''
                        body = ('import pathlib,subprocess,sys,time; '
                                f'subprocess.Popen([sys.executable,"-B","-c",{child!r}]{redirect}); '
                                f'p=pathlib.Path({str(ready)!r});\nwhile not p.exists(): time.sleep(.01)\n'
                                'print("owned descendant ready",flush=True); time.sleep(30)')
                    deadline = '.4' if trigger == 'timed_out' else 'None'
                    driver = ('import importlib.util,json,os,sys; from pathlib import Path; '
                              f's=importlib.util.spec_from_file_location("c",{str(ROOT / "scripts/ci-contract.py")!r}); '
                              'c=importlib.util.module_from_spec(s); s.loader.exec_module(c); c.TERMINATION_GRACE_SECONDS=.1; '
                              f'r=c.run_command([sys.executable,"-B","-c",{body!r}],Path({str(self.root / (name + ".log"))!r}),dict(os.environ),{deadline}); '
                              'print(json.dumps(r),flush=True)')
                    proc = subprocess.Popen([sys.executable, '-B', '-c', driver], stdout=subprocess.PIPE,
                                            stderr=subprocess.STDOUT, start_new_session=True)
                    try:
                        if trigger == 'cancelled':
                            expires = time.monotonic() + 5
                            while not ready.exists() and time.monotonic() < expires:
                                time.sleep(.01)
                            self.assertTrue(ready.exists(), 'owned child did not become ready')
                            proc.send_signal(signal.SIGTERM)
                        output = proc.communicate(timeout=8)[0].decode()
                        self.assertEqual(proc.returncode, 0, output)
                        result = json.loads(output.splitlines()[-1])
                        self.assertEqual(result['status'], trigger)
                        self.assertEqual(result['remainingLiveGroupMembers'], [])
                        self.assertFalse(ci.live_group_members(result['processGroup']))
                    finally:
                        if ready.exists():
                            owned_group = json.loads(ready.read_text())['pgid']
                            if ci.live_group_members(owned_group):
                                ci.signal_group(owned_group, signal.SIGKILL)
                        if proc.poll() is None:
                            proc.terminate()
                            try:
                                proc.communicate(timeout=3)
                            except subprocess.TimeoutExpired:
                                proc.kill()
                                proc.communicate(timeout=3)

    def test_owned_zombie_group_does_not_block_cleanup(self):
        proc = subprocess.Popen([sys.executable, '-B', '-c', 'pass'], start_new_session=True)
        try:
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                state = subprocess.check_output(['ps', '-p', str(proc.pid), '-o', 'stat='], text=True).strip()
                if state.startswith('Z'):
                    break
                time.sleep(.01)
            self.assertTrue(state.startswith('Z'), 'owned child did not reach zombie state: ' + state)
            self.assertEqual(ci.live_group_members(proc.pid), [])
            for sig in (0, signal.SIGTERM, signal.SIGKILL):
                ci.signal_group(proc.pid, sig)
        finally:
            proc.wait(timeout=5)

    def test_kernel_permission_error_for_empty_group_settles(self):
        observed = subprocess.CompletedProcess([], 0, stdout='123 456 Z\n124 456 X\n')
        with patch.object(ci.os, 'killpg', side_effect=PermissionError('owned zombie group')):
            with patch.object(ci.subprocess, 'run', return_value=observed):
                self.assertEqual(ci.live_group_members(456), [])
                for sig in (0, signal.SIGTERM, signal.SIGKILL):
                    ci.signal_group(456, sig)

    def test_kernel_permission_error_with_live_group_fails_cleanup(self):
        observed = subprocess.CompletedProcess([], 0, stdout='123 456 S\n124 456 Z\n')
        with patch.object(ci.os, 'killpg', side_effect=PermissionError('live group denied')):
            with patch.object(ci.subprocess, 'run', return_value=observed):
                self.assertEqual(ci.live_group_members(456), [123])
                for sig in (signal.SIGTERM, signal.SIGKILL):
                    with self.assertRaises(PermissionError):
                        ci.signal_group(456, sig)

    def test_failed_inventory_cannot_prove_empty_group(self):
        failure = subprocess.CalledProcessError(1, ['ps'])
        with patch.object(ci.os, 'killpg', side_effect=PermissionError('unverified group')):
            with patch.object(ci.subprocess, 'run', side_effect=failure):
                with self.assertRaises(subprocess.CalledProcessError):
                    ci.live_group_members(456)
                with self.assertRaises(subprocess.CalledProcessError):
                    ci.signal_group(456, signal.SIGKILL)



class RunLaneTests(unittest.TestCase):
    def run_lane(self, steps, keep_going=False):
        work = tempfile.TemporaryDirectory()
        self.addCleanup(work.cleanup)
        output = Path(work.name) / 'out'
        plan = {'lanes': {'demo': {'timeoutMinutes': 1, 'needs': [], 'rawDirectories': [], 'steps': steps}}}
        args = type('Args', (), {'lane': 'demo', 'output': output, 'local': False, 'keep_going': keep_going})()
        with patch.object(ci, 'PLAN', plan):
            status = ci.run_lane(args)
        return status, json.loads((output / 'report.json').read_text()), output

    def step(self, name, code, timeout=None):
        return {'name': name, 'command': [sys.executable, '-c', code], 'timeoutSeconds': timeout}

    def test_success_runs_every_step_in_order(self):
        status, report, output = self.run_lane([self.step('a', 'print(1)'), self.step('b', 'print(2)')])
        self.assertEqual((status, report['status']), (0, 'success'))
        self.assertEqual([s['name'] for s in report['steps']], ['a', 'b'])
        self.assertTrue((output / 'logs/01.log').exists())

    def test_architecture_restricted_step_is_skipped_elsewhere(self):
        other = 'arm64' if ci.HOST_ARCH == 'amd64' else 'amd64'
        skipped = dict(self.step('only-elsewhere', 'raise SystemExit(9)'), architectures=[other])
        ran = dict(self.step('here', 'print(1)'), architectures=[ci.HOST_ARCH])
        status, report, _ = self.run_lane([skipped, ran])
        self.assertEqual((status, report['status']), (0, 'success'))
        self.assertEqual([s['status'] for s in report['steps']], ['skipped', 'success'])

    def test_failure_stops_the_lane_unless_keep_going(self):
        steps = [self.step('bad', 'raise SystemExit(3)'), self.step('after', 'print(1)')]
        status, report, _ = self.run_lane(steps)
        self.assertEqual((status, report['status'], len(report['steps'])), (1, 'failure', 1))
        status, report, _ = self.run_lane(steps, keep_going=True)
        self.assertEqual((status, report['status'], len(report['steps'])), (1, 'failure', 2))

    def test_a_packed_job_runs_every_lane_and_summarizes_each(self):
        work = tempfile.TemporaryDirectory()
        self.addCleanup(work.cleanup)
        output = Path(work.name) / 'out'
        summary = Path(work.name) / 'summary.md'
        plan = {'lanes': {name: {'timeoutMinutes': 1, 'needs': [], 'rawDirectories': [],
                                 'steps': [self.step('only', code)]}
                          for name, code in (('first', 'raise SystemExit(3)'), ('second', 'print(2)'))}}
        args = argparse.Namespace(lanes=['first', 'second'], output=output, local=False, keep_going=False)
        with patch.object(ci, 'PLAN', plan), patch.dict(os.environ, {'GITHUB_STEP_SUMMARY': str(summary)}):
            status = ci.run_lanes(args)
        self.assertEqual(status, 1)
        self.assertEqual(json.loads((output / 'first/report.json').read_text())['status'], 'failure')
        self.assertEqual(json.loads((output / 'second/report.json').read_text())['status'], 'success')
        table = summary.read_text()
        self.assertIn('| first | failure |', table)
        self.assertIn('| only |', table)
        self.assertIn('| second | success |', table)

    def test_a_crashing_lane_fails_and_a_cancelled_lane_stops_the_job(self):
        work = tempfile.TemporaryDirectory()
        self.addCleanup(work.cleanup)
        output = Path(work.name) / 'out'
        summary = Path(work.name) / 'summary.md'
        ran = []

        def fake_run_lane(lane_args):
            ran.append(lane_args.lane)
            if lane_args.lane == 'crashes':
                raise RuntimeError('group did not settle')
            verdict = 'cancelled' if lane_args.lane == 'cancelled' else 'success'
            lane_args.output.mkdir(parents=True)
            (lane_args.output / 'report.json').write_text(json.dumps(
                {'status': 'success' if verdict == 'success' else 'failure',
                 'steps': [{'name': 'only', 'status': verdict}]}))
            return 0 if verdict == 'success' else 1

        args = argparse.Namespace(lanes=['crashes', 'passes', 'cancelled', 'never'], output=output,
                                  local=False, keep_going=False)
        with patch.object(ci, 'run_lane', fake_run_lane), \
                patch.dict(os.environ, {'GITHUB_STEP_SUMMARY': str(summary)}):
            status = ci.run_lanes(args)
        self.assertEqual(status, 1)
        self.assertEqual(ran, ['crashes', 'passes', 'cancelled'])
        table = summary.read_text()
        self.assertIn('| crashes | error |', table)
        self.assertIn('RuntimeError: group did not settle', table)
        self.assertIn('| passes | success |', table)
        self.assertIn('| cancelled | failure |', table)
        self.assertIn('| never | not run (cancelled) | - |', table)

    def test_step_timeout_fails_the_lane(self):
        status, report, _ = self.run_lane([self.step('slow', 'import time; time.sleep(30)', 0.1)])
        self.assertEqual((status, report['steps'][0]['status']), (1, 'timed_out'))


class PlanTests(unittest.TestCase):
    def test_every_planned_make_target_exists_exactly_once(self):
        makefile = (ROOT / 'Makefile').read_text()
        targets = [s['command'][1] for lane in ci.PLAN['lanes'].values()
                   for s in lane['steps'] if s['command'][0] == 'make']
        for target in targets:
            self.assertRegex(makefile, r'(?m)^(?:[\w.-]+ )*' + re.escape(target) + r'(?: [\w.-]+)*::?(?!=)',
                             'planned make target is not defined in the Makefile: ' + target)
        # A target may appear more than once only with different variables (the shards of
        # integration-test); the same invocation twice would run the same tests twice.
        invocations = [tuple(s['command'][1:]) for lane in ci.PLAN['lanes'].values()
                       for s in lane['steps'] if s['command'][0] == 'make']
        self.assertEqual(len(invocations), len(set(invocations)), 'a make invocation is planned in two lanes')
        for target in set(targets) - {'integration-test'}:
            self.assertEqual(targets.count(target), 1, 'a make target is planned in two lanes: ' + target)

    def test_integration_test_shards_are_planned_once_each(self):
        listed = subprocess.check_output([str(ROOT / 'scripts/integration-test.sh'), '--list'], text=True)
        shards = sorted({line.split()[0] for line in listed.splitlines()})
        steps = [s['command'] for lane in ci.PLAN['lanes'].values() for s in lane['steps']
                 if s['command'][:2] == ['make', 'integration-test']]
        # An unsharded step would run every package a second time.
        for command in steps:
            self.assertEqual(len(command), 3, command)
            self.assertRegex(command[2], r'^INTEGRATION_SHARD=\d+$', command)
        planned = sorted(command[2].split('=', 1)[1] for command in steps)
        self.assertEqual(planned, shards, 'every integration shard runs in exactly one lane')
        # The shard that holds ./internal/app runs the official Cargo replay on the
        # host, which needs the pinned Rust toolchain; the other shards need nothing.
        app_shard = next(line.split()[0] for line in listed.splitlines()
                         if line.split()[1] == './internal/app')
        for name, lane in ci.PLAN['lanes'].items():
            for step in lane['steps']:
                if step['command'][:2] == ['make', 'integration-test']:
                    shard = step['command'][2].split('=', 1)[1]
                    self.assertEqual('rust' in lane.get('needs', []), shard == app_shard,
                                     f'{name} runs shard {shard}; only the shard with ./internal/app needs rust')

    def test_every_notarx_gate_stays_planned(self):
        targets = {s['command'][1] for lane in ci.PLAN['lanes'].values()
                   for s in lane['steps'] if s['command'][0] == 'make'}
        required = {'openapi-check', 'cli-contract-check', 'cli-test', 'test', 'cargo-contract',
                    'kubernetes-local-check', 'email-compose-check', 'cli-runtime-e2e',
                    'native-oci-e2e', 'native-raw-e2e', 'native-maven-e2e', 'native-npm-e2e',
                    'native-pypi-e2e', 'native-go-e2e', 'native-apt-e2e', 'apt-signer-rotation-e2e',
                    'conan-e2e', 'backup-transfer-test', 'published-native-backup-test',
                    'integration-test', 'release-readiness-check',
                    'nexus-migration-client-e2e', 'nexus-inventory-scan-e2e', 'native-nuget-e2e',
                    'nexus-oci-migration-e2e', 'private-upstream-auth-e2e', 'private-upstream-formats-e2e',
                    'native-oci-discovery-e2e', 'nexus-npm-migration-e2e', 'native-helm-e2e',
                    'native-rubygems-e2e', 'nexus-pypi-migration-e2e'}
        self.assertEqual(required - targets, set())

    def test_lane_declarations_are_complete(self):
        self.assertNotIn('baseline', ci.PLAN)
        for name, lane in ci.PLAN['lanes'].items():
            with self.subTest(lane=name):
                self.assertRegex(name, r'^[a-z][a-z0-9-]*$')
                self.assertTrue(lane['steps'])
                self.assertIn(lane['arm64'], ci.SCOPES)
                self.assertGreater(lane['timeoutMinutes'], 0)
                self.assertLessEqual(lane['timeoutMinutes'], 60)
                self.assertLessEqual(set(lane['needs']), set(ci.TOOL_NEEDS))
                self.assertIsInstance(lane['images'], bool)
                names = [s['name'] for s in lane['steps']]
                self.assertEqual(len(names), len(set(names)))
                for step in lane['steps']:
                    self.assertIsInstance(step['command'], list)
                    limit = step['timeoutSeconds']
                    if limit is not None:
                        self.assertLessEqual(limit, lane['timeoutMinutes'] * 60)

    def test_notarx_step_timeouts_are_kept(self):
        steps = {s['name']: s for lane in ci.PLAN['lanes'].values() for s in lane['steps']}
        self.assertEqual(steps['published-native-backup-test']['timeoutSeconds'], 900)
        for name in ('nexus-migration-client-e2e', 'nexus-inventory-scan-e2e', 'nexus-oci-migration-e2e',
                     'private-upstream-auth-e2e'):
            self.assertEqual(steps[name]['timeoutSeconds'], 600)
        for name in ('native-nuget-e2e', 'private-upstream-formats-e2e', 'native-oci-discovery-e2e',
                     'nexus-npm-migration-e2e', 'native-helm-e2e', 'native-rubygems-e2e',
                     'nexus-pypi-migration-e2e'):
            self.assertEqual(steps[name]['timeoutSeconds'], 900)
        self.assertNotIn('upgrade-readiness', steps)  # ADR 0015 / #39
        self.assertNotIn('Prepare pinned Go modules', steps)  # the workflow warms modules once

    def test_matrix_is_generated_from_the_plan(self):
        def generate(*args):
            return json.loads(subprocess.check_output(
                [sys.executable, str(ROOT / 'scripts/ci-contract.py'), 'matrix', *args]))['include']

        for include in (generate(), generate('--scope', 'mainline'), generate('--scope', 'pull-request')):
            amd64 = [e for e in include if e['arch'] == 'amd64']
            # Every lane runs on amd64 exactly once, packed or alone.
            amd64_lanes = [lane for e in amd64 for lane in e['lanes'].split()]
            self.assertEqual(sorted(amd64_lanes), sorted(ci.PLAN['lanes']))
            # In plan order: the lanes of each job, the jobs in the order of their first lane.
            self.assertEqual(amd64_lanes, [lane for _, lanes in ci.jobs() for lane in lanes])
            self.assertEqual(len(amd64_lanes), len(set(amd64_lanes)))
            self.assertEqual([e['job'] for e in amd64], [name for name, _ in ci.jobs()])
            for entry in include:
                lanes = [ci.PLAN['lanes'][name] for name in entry['lanes'].split()]
                self.assertEqual(entry['timeout'], sum(lane['timeoutMinutes'] for lane in lanes))
                self.assertEqual(entry['runner'], ci.RUNNERS[entry['arch']])
                self.assertEqual(entry['images'], any(lane['images'] for lane in lanes))
                for need in ci.TOOL_NEEDS:
                    self.assertEqual(entry[need], any(need in lane['needs'] for lane in lanes))

    def test_groups_split_the_matrix_by_the_images_each_job_loads(self):
        def generate(*args):
            return json.loads(subprocess.check_output(
                [sys.executable, str(ROOT / 'scripts/ci-contract.py'), 'matrix', *args]))['include']

        for scope in (['--scope', 'mainline'], ['--scope', 'pull-request'], ['--scope', 'pull-request', '--no-arm64']):
            whole = generate(*scope)
            groups = {group: generate(*scope, '--group', group) for group in ci.GROUPS}
            # Every entry is in exactly one group, in plan order within the group.
            self.assertEqual(sorted(json.dumps(e, sort_keys=True) for g in groups.values() for e in g),
                             sorted(json.dumps(e, sort_keys=True) for e in whole))
            for group, entries in groups.items():
                self.assertEqual(entries, [e for e in whole if ci.group_of(e) == group])
            # early jobs load no image; amd64 and arm64 jobs wait for their own build.
            self.assertTrue(all(not e['images'] for e in groups['early']))
            self.assertTrue(all(e['images'] and e['arch'] == 'amd64' for e in groups['amd64']))
            self.assertTrue(all(e['images'] and e['arch'] == 'arm64' for e in groups['arm64']))
        # A job is early only when none of its lanes loads the images.
        early = {lane for e in generate('--group', 'early') for lane in e['lanes'].split()}
        self.assertTrue(early)
        self.assertEqual(early, {name for name, lanes in ci.jobs() for name in lanes
                                 if not any(ci.PLAN['lanes'][lane]['images'] for lane in lanes)})
        self.assertEqual(generate('--scope', 'pull-request', '--no-arm64', '--group', 'arm64'), [])
        refused = subprocess.run([sys.executable, str(ROOT / 'scripts/ci-contract.py'), 'matrix', '--group', 'x'],
                                 capture_output=True, text=True)
        self.assertEqual(refused.returncode, 2, refused.stdout + refused.stderr)

    def test_every_contract_group_job_passes_every_lane_input(self):
        # The three group jobs call contract-lane.yml with the same inputs; one that
        # missed an input would silently run with its default (images defaults to true).
        suite = (ROOT / '.github/workflows/test-suite.yml').read_text()
        lane_workflow = (ROOT / '.github/workflows/contract-lane.yml').read_text()
        inputs_block = lane_workflow.split('    inputs:\n', 1)[1].split('\npermissions:', 1)[0]
        inputs = set(re.findall(r'(?m)^      ([a-z][\w-]*):$', inputs_block))
        self.assertIn('images', inputs)
        callers = [(name, body) for name, body in
                   re.findall(r'(?ms)^  (contract-[a-z0-9]+):\n(.*?)(?=^  [a-z][\w-]*:\n|\Z)', suite)
                   if 'contract-lane.yml' in body]
        self.assertEqual(sorted(name for name, _ in callers), sorted('contract-' + g for g in ci.GROUPS))
        for name, body in callers:
            with self.subTest(job=name):
                self.assertIn('uses: ./.github/workflows/contract-lane.yml', body)
                passed = set(re.findall(r'(?m)^      ([a-z][\w-]*): ', body.split('    with:\n', 1)[1]))
                self.assertEqual(passed, inputs)
                group = name.split('-', 1)[1]
                self.assertIn('fromJSON(needs.contract-plan.outputs.' + group + ')', body)
                self.assertIn("if: needs.contract-plan.outputs." + group + "-jobs != '0'", body)

    def test_packed_jobs_follow_the_plan(self):
        packs = ci.PLAN['jobs']
        packed = [lane for lanes in packs.values() for lane in lanes]
        self.assertEqual(len(packed), len(set(packed)), 'a lane is packed into two jobs')
        self.assertTrue(set(packed) <= set(ci.PLAN['lanes']))
        self.assertFalse(set(packs) & set(ci.PLAN['lanes']), 'a job may not take the name of a lane')
        for name, lanes in ci.jobs():
            self.assertEqual(lanes, packs.get(name, [name]))
        for broken in ({'x': ['core-email'], 'y': ['core-email']}, {'x': ['no-such-lane']}, {'core-email': ['helm']}):
            with patch.object(ci, 'PLAN', dict(ci.PLAN, jobs=broken)), self.assertRaises(ValueError):
                ci.jobs()

    def test_arm64_legs_follow_the_plan_tiers(self):
        mainline = [lane for e in json.loads(subprocess.check_output(
            [sys.executable, str(ROOT / 'scripts/ci-contract.py'), 'matrix', '--scope', 'mainline']))['include']
            if e['arch'] == 'arm64' for lane in e['lanes'].split()]
        self.assertEqual(mainline, [lane for _, lanes in ci.jobs() for lane in lanes])
        pull_request = [lane for e in json.loads(subprocess.check_output(
            [sys.executable, str(ROOT / 'scripts/ci-contract.py'), 'matrix', '--scope', 'pull-request']))['include']
            if e['arch'] == 'arm64' for lane in e['lanes'].split()]
        # ADR 0016: every core lane plus the native-format smoke subset on each
        # architecture-sensitive pull request; a packed job's arm64 leg runs only those.
        self.assertEqual(pull_request, [lane for _, lanes in ci.jobs() for lane in lanes
                                        if ci.PLAN['lanes'][lane]['arm64'] == 'pull-request'])
        self.assertEqual(sorted(pull_request), sorted(['core-checks', 'core-email', 'core-native-e2e', 'core-backup',
                                                       'core-integration-1', 'core-integration-2',
                                                       'core-integration-3',
                                                       'six-format-auth', 'oci-discovery']))
        self.assertEqual({lane['arm64'] for lane in ci.PLAN['lanes'].values()}, {'pull-request', 'mainline'})
        # ADR 0016, amendment of 2026-11-19: a pull request that changes nothing
        # architecture-sensitive runs no arm64 lane; the mainline scope cannot drop them.
        amd64_only = json.loads(subprocess.check_output(
            [sys.executable, str(ROOT / 'scripts/ci-contract.py'), 'matrix', '--scope', 'pull-request',
             '--no-arm64']))['include']
        self.assertEqual(sorted(lane for e in amd64_only for lane in e['lanes'].split()), sorted(ci.PLAN['lanes']))
        self.assertEqual({e['arch'] for e in amd64_only}, {'amd64'})
        refused = subprocess.run([sys.executable, str(ROOT / 'scripts/ci-contract.py'), 'matrix', '--scope',
                                  'mainline', '--no-arm64'], capture_output=True, text=True)
        self.assertEqual(refused.returncode, 2, refused.stdout + refused.stderr)
        with self.assertRaises(ValueError):
            ci.matrix('mainline', arm64=False)
        self.assertEqual({e['runner'] for e in json.loads(subprocess.check_output(
            [sys.executable, str(ROOT / 'scripts/ci-contract.py'), 'matrix']))['include']},
            {'ubuntu-24.04', 'ubuntu-24.04-arm'})

    def test_workflows_name_no_lane_and_pin_no_upstream(self):
        for path in ('ci.yml', 'test-suite.yml', 'release.yml', 'contract-lane.yml'):
            text = (ROOT / '.github/workflows' / path).read_text()
            for lane in list(ci.LANES) + list(ci.PLAN.get('jobs', {})):
                self.assertNotRegex(text, r'(?<![\w-])' + re.escape(lane) + r'(?![\w-])', path + ' names lane ' + lane)
            self.assertNotIn('cc' + 'sert', text)
        lane_workflow = (ROOT / '.github/workflows/contract-lane.yml').read_text()
        self.assertIn('timeout-minutes: ${{ inputs.timeout }}', lane_workflow)
        self.assertIn('runs-on: ${{ inputs.runner }}', lane_workflow)
        self.assertNotIn('ubuntu-latest', lane_workflow)
        self.assertNotIn('fetch-depth', lane_workflow)  # default shallow checkout
        self.assertFalse((ROOT / '.github/workflows/contract-benchmark.yml').exists())

    def test_pushes_to_main_run_no_tests_and_one_suite_is_shared(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text()
        suite = (ROOT / '.github/workflows/test-suite.yml').read_text()
        release = (ROOT / '.github/workflows/release.yml').read_text()
        # ADR 0024 amendment of 2026-11-19: a push to main only publishes. The test suite is
        # called by pull requests (ci.yml) and by the release gates (release.yml) alone.
        self.assertEqual(workflow.count('    needs: mainline-plan\n'), 2)
        self.assertIn('uses: ./.github/workflows/release-archives.yml', workflow)
        self.assertIn('uses: ./.github/workflows/image-build.yml', workflow)
        self.assertIn("  tests:\n    needs: changes\n    if: github.event_name == 'pull_request'", workflow)
        self.assertIn("  changes:\n    if: github.event_name == 'pull_request'", workflow)
        self.assertEqual(workflow.count('uses: ./.github/workflows/test-suite.yml'), 1)
        self.assertEqual(release.count('uses: ./.github/workflows/test-suite.yml'), 1)
        self.assertNotIn('platforms: linux/amd64,linux/arm64', workflow)
        self.assertNotIn('continue-on-error', workflow + suite + release)
        for group in ci.GROUPS:
            self.assertIn('fromJSON(needs.contract-plan.outputs.' + group + ')', suite)
        self.assertIn('sha: ${{ inputs.sha }}', suite)

    def test_toolchains_are_read_from_the_repository(self):
        self.assertIn('1.96.0', (ROOT / 'rust-toolchain.toml').read_text())
        self.assertEqual((ROOT / '.nvmrc').read_text().strip(), '26')
        lane_workflow = (ROOT / '.github/workflows/contract-lane.yml').read_text()
        self.assertIn('node-version-file: .nvmrc', lane_workflow)
        self.assertIn('go-version-file: go.mod', lane_workflow)
        self.assertNotRegex(lane_workflow, r'toolchain:\s*\d')

    def test_local_entrypoint_runs_every_planned_lane_in_order(self):
        with tempfile.TemporaryDirectory() as directory:
            binary = Path(directory) / 'bin'
            binary.mkdir()
            trace = Path(directory) / 'trace'
            worker = binary / 'python3'
            worker.write_text(f'#!{sys.executable}\n'
                              'import os,sys\n'
                              "if sys.argv[1:2] == ['-c'] or sys.argv[2:3] == ['list']:\n"
                              f'    os.execv({sys.executable!r}, [{sys.executable!r}, *sys.argv[1:]])\n'
                              f'with open({str(trace)!r}, "a") as output: output.write(sys.argv[3]+"\\n")\n'
                              'sys.stdin.read()\n')
            worker.chmod(0o700)
            make = binary / 'make'
            make.write_text('#!/bin/sh\nexit 0\n')
            make.chmod(0o700)
            env = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ['PATH'])
            result = subprocess.run([str(ROOT / 'scripts/ci-local.sh'), '--job', 'contract'], cwd=ROOT,
                                    env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=15)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(trace.read_text().splitlines(), list(ci.LANES))


class ToolchainPinTests(unittest.TestCase):
    """Every place that names a toolchain version must agree (docs/ci.md, Dependency updates)."""
    def test_go_images_match_go_mod_and_each_other(self):
        go_version = re.search(r'^go (\S+)$', (ROOT / 'go.mod').read_text(), re.M).group(1)
        pinned = {}
        for name in ['Dockerfile', 'Dockerfile.notarxctl', 'Dockerfile.apt-signer', 'Dockerfile.scanner',
                     'compose.integration.yml', 'Makefile']:
            for image in re.findall(r'golang:[^\s]+@sha256:[0-9a-f]{64}', (ROOT / name).read_text()):
                pinned.setdefault(image, []).append(name)
        self.assertEqual(len(pinned), 1, pinned)
        self.assertTrue(next(iter(pinned)).startswith(f'golang:{go_version}-alpine@sha256:'), (go_version, pinned))

    def test_console_image_uses_the_node_major_of_nvmrc(self):
        major = (ROOT / '.nvmrc').read_text().strip()
        first = (ROOT / 'Dockerfile.console').read_text().splitlines()[0]
        self.assertRegex(first, rf'node:{major}\.\d+\.\d+-alpine@sha256:')
        for name in ['ci.yml', 'release.yml']:
            self.assertNotRegex((ROOT / '.github/workflows' / name).read_text(), r'node-version:\s*\d')


if __name__ == '__main__':
    unittest.main()
