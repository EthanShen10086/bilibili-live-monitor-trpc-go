#!/usr/bin/env python3
"""Smoke both binaries in temporary roots, outside the schedule. No real sends."""
import datetime, json, pathlib, platform, socket, subprocess, tempfile, time, urllib.request, urllib.error
repo = pathlib.Path(__file__).resolve().parents[1]
go = repo
framework = (repo / 'trpc_go.yaml').exists()
for binary in ('monitor',):
    with tempfile.TemporaryDirectory(prefix='live-monitor-smoke-') as tmp:
        root = pathlib.Path(tmp)
        today = datetime.datetime.now(datetime.timezone(datetime.timedelta(hours=8))).isoweekday()
        excluded_today = today % 7 + 1
        cfg = (go / 'config.yaml').read_text().replace('weekdays: [3, 5, 6, 7]', f'weekdays: [{excluded_today}]')
        cfg = cfg.replace('active: local', 'active: cloud')
        (root / 'config.yaml').write_text(cfg)
        env = root / '.env'
        env.write_text('FEISHU_WEBHOOK=https://open.feishu.cn/open-apis/bot/v2/hook/smoke\nFEISHU_WEBHOOK_SECRET=smoke-secret\n')
        env.chmod(0o600)
        ports = []
        reserved = [socket.socket(), socket.socket()]
        try:
            for sock in reserved:
                sock.bind(('127.0.0.1', 0)); ports.append(sock.getsockname()[1])
        finally:
            for sock in reserved: sock.close()
        if framework:
            trpc = (go / 'trpc_go.yaml').read_text().replace('19028', str(ports[0])).replace('19029', str(ports[1]))
            (root / 'trpc_go.yaml').write_text(trpc)
        with (root / 'process.log').open('w+') as log:
            p = subprocess.Popen([str(go / 'dist' / binary), '--root', str(root), 'run', '--managed', 'cloud'], stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 20
                while True:
                    if p.poll() is not None:
                        log.seek(0); raise RuntimeError(log.read())
                    try:
                        state = json.loads((root / 'var/status.json').read_text())
                        if state['running'] and state['detector_state'] == 'outside_window': break
                    except (FileNotFoundError, json.JSONDecodeError): pass
                    if time.monotonic() > deadline: raise RuntimeError('worker startup timed out')
                    time.sleep(.1)
                if framework:
                    while True:
                        try:
                            with urllib.request.urlopen(f'http://127.0.0.1:{ports[1]}/healthz', timeout=5) as response:
                                assert response.status == 200 and response.read() == b'ok\n'
                            break
                        except urllib.error.URLError:
                            if p.poll() is not None or time.monotonic() > deadline:
                                raise
                            time.sleep(.1)
                    with urllib.request.urlopen(f'http://127.0.0.1:{ports[1]}/status', timeout=5) as response:
                        assert json.load(response)['detector_state'] == 'outside_window'
                second = subprocess.run([str(go / 'dist/monitor'), '--root', str(root), 'run', '--managed', 'cloud'], capture_output=True, timeout=5)
                assert second.returncode != 0 and b'ELOCKED' in (root / 'var/error.log').read_bytes()
                p.terminate(); assert p.wait(timeout=40) == 0
                assert not json.loads((root / 'var/status.json').read_text())['running']
                assert not (root / 'var/instance.guard.lock').exists()
                print(binary + ': startup, exclusive lock, graceful exit' + (', real framework HTTP' if framework else '') + ' passed')
            finally:
                if p.poll() is None:
                    p.terminate()
                    try: p.wait(timeout=40)
                    except subprocess.TimeoutExpired: p.kill(); p.wait()
