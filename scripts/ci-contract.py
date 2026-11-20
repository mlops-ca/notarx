#!/usr/bin/env python3
"""Run the CI contract lanes declared in scripts/ci-contract-lanes.json.

The lane plan is the single source of truth: lane names, order, tool needs,
timeouts, steps and failure artefacts all live there. The workflows read it
through the `matrix` command; nothing else names a lane.
"""
import argparse
import hashlib
import json
import os
import platform
import selectors
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PLAN = json.loads((ROOT / 'scripts/ci-contract-lanes.json').read_text())
LANES = tuple(PLAN['lanes'])
TOOL_NEEDS = ('rust', 'node', 'npm')
TERMINATION_GRACE_SECONDS = 45


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def openapi_fresh():
    paths = ['api/openapi/native-hosted-v1.json', 'api/openapi/management-runtime-v1.json',
             'internal/admin/openapi/generated.go', 'console/src/client/sdk.gen.ts',
             'console/src/client/types.gen.ts']
    before = {p: sha(ROOT / p) for p in paths}
    subprocess.run(['make', 'openapi-generate-admin'], cwd=ROOT, check=True)
    subprocess.run(['npm', '--prefix', 'console', 'run', 'generate:api'], cwd=ROOT, check=True)
    if before != {p: sha(ROOT / p) for p in paths}:
        raise RuntimeError('generated OpenAPI artifacts were stale')


def live_group_members(pgid):
    """Only inspect the group created by this supervisor; zombies cannot run."""
    result = subprocess.run(['ps', '-A', '-o', 'pid=,pgid=,stat='],
                            capture_output=True, text=True, check=True, timeout=5)
    members = []
    for line in result.stdout.splitlines():
        fields = line.split()
        if len(fields) >= 3 and int(fields[1]) == pgid and not fields[2].startswith(('Z', 'X')):
            members.append(int(fields[0]))
    return members


def signal_group(pgid, sig):
    try:
        os.killpg(pgid, sig)
    except ProcessLookupError:
        pass
    except PermissionError:
        # Darwin returns EPERM for groups containing only zombies. A denied
        # signal is harmless only when a fresh inventory proves none can run.
        if live_group_members(pgid):
            raise


def run_command(command, log, env, timeout=None):
    """A closed pipe or exited leader cannot end supervision of a live group."""
    started = time.monotonic()
    proc = subprocess.Popen(command, cwd=ROOT, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, start_new_session=True)
    stopped = killed = None
    reason = None

    def stop(signum, _frame=None):
        nonlocal stopped, reason
        if stopped is None:
            reason = 'cancelled' if signum else 'timed_out'
            stopped = time.monotonic()
            signal_group(proc.pid, signal.SIGTERM)

    previous = {sig: signal.signal(sig, stop) for sig in (signal.SIGINT, signal.SIGTERM)}
    selector = selectors.DefaultSelector()
    selector.register(proc.stdout, selectors.EVENT_READ)
    try:
        with log.open('wb') as output:
            while True:
                running = proc.poll() is None
                alive = running or bool(live_group_members(proc.pid))
                if not selector.get_map() and not alive:
                    break
                tick = time.monotonic()
                if timeout and tick - started >= timeout and stopped is None:
                    stop(0)
                elif not running and alive and stopped is None:
                    stopped = tick
                    reason = 'owned_processes_remaining'
                    signal_group(proc.pid, signal.SIGTERM)
                if stopped is not None and tick - stopped >= TERMINATION_GRACE_SECONDS and killed is None:
                    signal_group(proc.pid, signal.SIGKILL)
                    killed = tick
                if killed is not None and tick - killed > 5:
                    raise RuntimeError('owned command group or output pipe did not settle after SIGKILL')
                if not selector.get_map():
                    time.sleep(0.05)
                    continue
                for key, _ in selector.select(0.1):
                    data = os.read(key.fileobj.fileno(), 65536)
                    if not data:
                        selector.unregister(key.fileobj)
                        continue
                    output.write(data)
                    output.flush()
                    sys.stdout.buffer.write(data)
                    sys.stdout.buffer.flush()
            code = proc.wait()
    finally:
        selector.close()
        proc.stdout.close()
        # Exception paths also terminate descendants after the leader exits.
        try:
            if proc.poll() is None or live_group_members(proc.pid):
                signal_group(proc.pid, signal.SIGKILL)
            proc.wait()
            settle_deadline = time.monotonic() + 5
            while live_group_members(proc.pid):
                if time.monotonic() >= settle_deadline:
                    raise RuntimeError('owned process group still contains running descendants')
                time.sleep(0.05)
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)
    return {'command': command, 'exitCode': code,
            'status': reason or ('success' if code == 0 else 'failure'),
            'durationSeconds': time.monotonic() - started, 'timeoutSeconds': timeout,
            'processGroup': proc.pid, 'remainingLiveGroupMembers': []}


HOST_ARCH = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(
    platform.machine().lower(), platform.machine().lower())
RUNNERS = {'amd64': 'ubuntu-24.04', 'arm64': 'ubuntu-24.04-arm'}
SCOPES = ('pull-request', 'mainline')


def jobs():
    """The CI jobs of the plan, in lane order: (name, lanes).

    `jobs` in the plan packs short lanes into one job that shares a single setup
    (checkout, toolchains, image load); every other lane is a job of its own,
    named after the lane. A lane is in at most one job.
    """
    packs = PLAN.get('jobs', {})
    owner = {}
    for name, lanes in packs.items():
        if name in PLAN['lanes']:
            raise ValueError('job ' + name + ' has the name of a lane')
        for lane in lanes:
            if lane not in PLAN['lanes']:
                raise ValueError('job ' + name + ' packs unknown lane ' + lane)
            if lane in owner:
                raise ValueError('lane ' + lane + ' is packed into ' + owner[lane] + ' and ' + name)
            owner[lane] = name
    result, seen = [], set()
    for lane in PLAN['lanes']:
        name = owner.get(lane, lane)
        if name not in seen:
            seen.add(name)
            result.append((name, list(packs.get(name, [lane]))))
    return result


def matrix(scope='mainline', arm64=True):
    """The GitHub Actions matrix, straight from the plan (ADR 0016).

    Every lane runs on amd64. A lane also runs natively on arm64 when its
    `arm64` tier is `pull-request` or the scope is `mainline` (the release
    gates: every lane joins). A pull request that changes nothing
    architecture-sensitive (`arm64=False`, the arm64 class of
    scripts/ci-changes.sh) runs no arm64 lane; ADR 0016, amendment of 2026-11-19.
    The mainline scope always runs every arm64 lane. Nothing is emulated.
    """
    if scope == 'mainline' and not arm64:
        raise ValueError('the mainline scope runs every arm64 lane; --no-arm64 is for pull requests')
    def entry(name, lanes, arch):
        plans = [PLAN['lanes'][lane] for lane in lanes]
        return {'job': name, 'lanes': ' '.join(lanes), 'timeout': sum(p['timeoutMinutes'] for p in plans),
                'arch': arch, 'runner': RUNNERS[arch],
                **{need: any(need in p['needs'] for p in plans) for need in TOOL_NEEDS}}

    def on_arm64(lane):
        return scope == 'mainline' or (arm64 and PLAN['lanes'][lane]['arm64'] == 'pull-request')

    include = [entry(name, lanes, 'amd64') for name, lanes in jobs()]
    # A packed job's arm64 leg runs only the lanes of the job that run on arm64.
    for name, lanes in jobs():
        arm64_lanes = [lane for lane in lanes if on_arm64(lane)]
        if arm64_lanes:
            include.append(entry(name, arm64_lanes, 'arm64'))
    return {'include': include}


def run_lane(args):
    lane = PLAN['lanes'][args.lane]
    output = args.output
    output.mkdir(parents=True, exist_ok=True)
    (output / 'logs').mkdir(exist_ok=True)
    raw_before = {str(p) for directory in lane['rawDirectories'] for p in (ROOT / directory).glob('*')}
    work = Path(tempfile.mkdtemp(prefix='notarx-contract-' + args.lane + '-'))
    env = dict(os.environ, TMPDIR=str(work))
    report = {'lane': args.lane, 'status': 'failure', 'steps': []}
    try:
        for index, step in enumerate(lane['steps']):
            command = step['command']
            # A step that executes a third-party artifact published for one architecture
            # only declares it; it is skipped (and reported) elsewhere, never emulated.
            if 'architectures' in step and HOST_ARCH not in step['architectures']:
                print('Contract lane ' + args.lane + ': ' + step['name'] + ' skipped on ' + HOST_ARCH
                      + ' (amd64-only artifact)', flush=True)
                report['steps'].append({'name': step['name'], 'status': 'skipped', 'architecture': HOST_ARCH})
                continue
            if args.local and command == ['make', 'openapi-check']:
                command = [sys.executable, str(Path(__file__).resolve()), 'openapi-fresh']
            print('Contract lane ' + args.lane + ': ' + step['name'], flush=True)
            result = run_command(command, output / 'logs' / f'{index:02d}.log', env, step.get('timeoutSeconds'))
            result['name'] = step['name']
            report['steps'].append(result)
            if result['status'] != 'success' and not args.keep_going:
                break
        if len(report['steps']) == len(lane['steps']) and all(s['status'] in ('success', 'skipped') for s in report['steps']):
            report['status'] = 'success'
    finally:
        for directory in lane['rawDirectories']:
            for path in (ROOT / directory).glob('*'):
                if str(path) not in raw_before:
                    target = output / 'raw' / Path(directory).name / path.name
                    if path.is_dir():
                        shutil.copytree(path, target, dirs_exist_ok=True)
                    else:
                        target.parent.mkdir(parents=True, exist_ok=True)
                        shutil.copy2(path, target)
        shutil.rmtree(work, ignore_errors=True)
        (output / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    return 0 if report['status'] == 'success' else 1


def run_lanes(args):
    """Run each lane of a CI job in order; a failed lane does not stop the next one.

    One lane writes its evidence into --output as before; several lanes write one
    subdirectory each. The per-lane verdict goes to the job's step summary.
    """
    rows, status = [], 0
    for name in args.lanes:
        output = args.output if len(args.lanes) == 1 else args.output / name
        lane_args = type('LaneArgs', (), {'lane': name, 'output': output, 'local': args.local,
                                          'keep_going': args.keep_going})()
        started = time.monotonic()
        code = run_lane(lane_args)
        status = status or code
        report = json.loads((output / 'report.json').read_text())
        failed = [s['name'] for s in report['steps'] if s['status'] not in ('success', 'skipped')]
        rows.append((name, report['status'], time.monotonic() - started, ', '.join(failed)))
    for name, verdict, seconds, failed in rows:
        print(f'Contract lane {name}: {verdict} in {seconds / 60:.1f} min' + (f' (failed: {failed})' if failed else ''),
              flush=True)
    summary = os.environ.get('GITHUB_STEP_SUMMARY')
    if summary:
        with open(summary, 'a', encoding='utf-8') as handle:
            handle.write('| Contract lane | Result | Minutes | Failed steps |\n| --- | --- | --- | --- |\n')
            for name, verdict, seconds, failed in rows:
                handle.write(f'| {name} | {verdict} | {seconds / 60:.1f} | {failed or "-"} |\n')
    return status


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    lane = commands.add_parser('run')
    lane.add_argument('lanes', nargs='+', choices=LANES, metavar='lane')
    lane.add_argument('--output', required=True, type=Path)
    lane.add_argument('--local', action='store_true')
    lane.add_argument('--keep-going', action='store_true')
    commands.add_parser('openapi-fresh')
    commands.add_parser('list')
    plan_matrix = commands.add_parser('matrix')
    plan_matrix.add_argument('--scope', choices=SCOPES, default='mainline')
    plan_matrix.add_argument('--no-arm64', dest='arm64', action='store_false',
                             help='pull-request scope only: the change is not architecture-sensitive')
    args = parser.parse_args()
    if args.command == 'run':
        if len(set(args.lanes)) != len(args.lanes):
            parser.error('a lane is named twice')
        return run_lanes(args)
    if args.command == 'openapi-fresh':
        openapi_fresh()
    elif args.command == 'list':
        print('\n'.join(LANES))
    else:
        try:
            plan = matrix(args.scope, args.arm64)
        except ValueError as error:
            parser.error(str(error))
        print(json.dumps(plan))
    return 0


if __name__ == '__main__':
    sys.exit(main())
