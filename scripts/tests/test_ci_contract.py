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

    def test_failure_stops_the_lane_unless_keep_going(self):
        steps = [self.step('bad', 'raise SystemExit(3)'), self.step('after', 'print(1)')]
        status, report, _ = self.run_lane(steps)
        self.assertEqual((status, report['status'], len(report['steps'])), (1, 'failure', 1))
        status, report, _ = self.run_lane(steps, keep_going=True)
        self.assertEqual((status, report['status'], len(report['steps'])), (1, 'failure', 2))

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
        self.assertEqual(len(targets), len(set(targets)), 'a make target is planned in two lanes')

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
                self.assertGreater(lane['timeoutMinutes'], 0)
                self.assertLessEqual(lane['timeoutMinutes'], 60)
                self.assertLessEqual(set(lane['needs']), set(ci.TOOL_NEEDS))
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
        matrix = json.loads(subprocess.check_output([sys.executable, str(ROOT / 'scripts/ci-contract.py'), 'matrix']))
        self.assertEqual([e['lane'] for e in matrix['include']], list(ci.PLAN['lanes']))
        for entry in matrix['include']:
            lane = ci.PLAN['lanes'][entry['lane']]
            self.assertEqual(entry['timeout'], lane['timeoutMinutes'])
            for need in ci.TOOL_NEEDS:
                self.assertEqual(entry[need], need in lane['needs'])

    def test_workflows_name_no_lane_and_pin_no_upstream(self):
        for path in ('ci.yml', 'contract-lane.yml'):
            text = (ROOT / '.github/workflows' / path).read_text()
            for lane in ci.LANES:
                self.assertNotRegex(text, r'(?<![\w-])' + re.escape(lane) + r'(?![\w-])', path + ' names lane ' + lane)
            self.assertNotIn('ccsert', text)
        lane_workflow = (ROOT / '.github/workflows/contract-lane.yml').read_text()
        self.assertIn('timeout-minutes: ${{ inputs.timeout }}', lane_workflow)
        self.assertNotIn('ubuntu-latest', lane_workflow)
        self.assertNotIn('fetch-depth', lane_workflow)  # default shallow checkout
        self.assertFalse((ROOT / '.github/workflows/contract-benchmark.yml').exists())

    def test_workflow_keeps_stable_release_gate_names(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text()
        self.assertIn('  contract-and-test:\n    if: always()', workflow)
        self.assertEqual(workflow.count('    needs: [contract-and-test, quality, console, console-e2e]'), 2)
        self.assertNotIn('continue-on-error', workflow)
        self.assertIn('fromJSON(needs.contract-plan.outputs.matrix)', workflow)

    def test_toolchains_are_read_from_the_repository(self):
        self.assertIn('1.96.0', (ROOT / 'rust-toolchain.toml').read_text())
        self.assertEqual((ROOT / '.nvmrc').read_text().strip(), '24')
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


if __name__ == '__main__':
    unittest.main()
