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


def matrix(scope='mainline'):
    """The GitHub Actions matrix, straight from the plan (ADR 0016).

    Every lane runs on amd64. A lane also runs natively on arm64 when its
    `arm64` tier is `pull-request` (every pull request) or the scope is
    `mainline` (pushes to main: the remaining lanes join). Nothing is emulated.
    """
    def entry(name, lane, arch):
        return {'lane': name, 'timeout': lane['timeoutMinutes'], 'arch': arch, 'runner': RUNNERS[arch],
                **{need: need in lane['needs'] for need in TOOL_NEEDS}}

    include = [entry(name, lane, 'amd64') for name, lane in PLAN['lanes'].items()]
    include += [entry(name, lane, 'arm64') for name, lane in PLAN['lanes'].items()
                if scope == 'mainline' or lane['arm64'] == 'pull-request']
    return {'include': include}


def run_lane(args):
    lane = PLAN['lanes'][args.lane]
    output = args.output
    output.mkdir(parents=True, exist_ok=True)
    (output / 'logs').mkdir(exist_ok=True)
    raw_before = {str(p) for directory in lane['rawDirectories'] for p in (ROOT / directory).glob('*')}
    work = Path(tempfile.mkdtemp(prefix='ag-contract-' + args.lane + '-'))
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    lane = commands.add_parser('run')
    lane.add_argument('lane', choices=LANES)
    lane.add_argument('--output', required=True, type=Path)
    lane.add_argument('--local', action='store_true')
    lane.add_argument('--keep-going', action='store_true')
    commands.add_parser('openapi-fresh')
    commands.add_parser('list')
    plan_matrix = commands.add_parser('matrix')
    plan_matrix.add_argument('--scope', choices=SCOPES, default='mainline')
    args = parser.parse_args()
    if args.command == 'run':
        return run_lane(args)
    if args.command == 'openapi-fresh':
        openapi_fresh()
    elif args.command == 'list':
        print('\n'.join(LANES))
    else:
        print(json.dumps(matrix(args.scope)))
    return 0


if __name__ == '__main__':
    sys.exit(main())
