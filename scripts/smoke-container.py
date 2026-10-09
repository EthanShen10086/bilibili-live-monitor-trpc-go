#!/usr/bin/env python3
"""Linux host-network container smoke. Dummy credentials, outside schedule."""
import datetime, json, pathlib, socket, subprocess, tempfile, time, urllib.request, uuid
repo = pathlib.Path(__file__).resolve().parents[1]
name = 'bili-smoke-' + uuid.uuid4().hex[:12]
volume = name + '-state'
def docker(*args):
    return subprocess.check_output(['docker', *args], text=True, stderr=subprocess.STDOUT).strip()
with tempfile.TemporaryDirectory(prefix=name) as tmp:
    root = pathlib.Path(tmp)
    today = datetime.datetime.now(datetime.timezone(datetime.timedelta(hours=8))).isoweekday()
    config = (repo / 'deploy/cloud/config.light.yaml').read_text().replace('weekdays: [3, 5, 6, 7]', f'weekdays: [{today % 7 + 1}]')
    (root / 'config.yaml').write_text(config)
    sockets = [socket.socket(), socket.socket()]
    for sock in sockets: sock.bind(('127.0.0.1', 0))
    ports = [sock.getsockname()[1] for sock in sockets]
    for sock in sockets: sock.close()
    (root / 'trpc_go.yaml').write_text((repo / 'trpc_go.yaml').read_text().replace('19028',str(ports[0])).replace('19029',str(ports[1])))
    try:
        docker('run','-d','--name',name,'--init','--network','host','--read-only','--cap-drop','ALL',
               '--security-opt','no-new-privileges:true','--memory','256m','--cpus','0.5','--tmpfs','/tmp:size=16m',
               '--env','FEISHU_WEBHOOK=https://open.feishu.cn/open-apis/bot/v2/hook/smoke',
               '--env','FEISHU_WEBHOOK_SECRET=smoke-secret',
               '--mount',f'type=bind,src={root}/config.yaml,dst=/app/config.yaml,readonly',
               '--mount',f'type=bind,src={root}/trpc_go.yaml,dst=/app/trpc_go.yaml,readonly',
               '--mount',f'type=volume,src={volume},dst=/app/var',
               '--health-cmd','/app/monitor --root /app healthcheck live',
               '--health-interval','2s','--health-timeout','3s','--health-retries','3',
               'live-monitor-test')
        deadline=time.monotonic()+45
        while docker('inspect','--format','{{.State.Health.Status}}',name)!='healthy':
            if docker('inspect','--format','{{.State.Running}}',name)!='true' or time.monotonic()>deadline:
                raise RuntimeError(docker('logs',name))
            time.sleep(.25)
        for path in ('livez','readyz','healthz','status','metrics'):
            with urllib.request.urlopen(f'http://127.0.0.1:{ports[1]}/{path}',timeout=5) as response:
                body=response.read();assert response.status==200
                if path=='metrics': assert b'live_monitor_last_progress_timestamp_seconds' in body
                if path=='status': assert json.loads(body)['detector_state']=='outside_window'
        docker('stop','--time','80',name)
        assert docker('inspect','--format','{{.State.ExitCode}}',name)=='0'
        assert 'worker_starting' in docker('logs',name)
        print('container: non-root read-only startup, healthcheck, metrics, graceful exit passed')
    finally:
        subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        subprocess.run(['docker','volume','rm',volume],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
