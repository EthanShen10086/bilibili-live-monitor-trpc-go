#!/usr/bin/env python3
"""Optional actual HTTPS proxy smoke, using fake credentials and a temporary worker."""
import base64, datetime, json, os, pathlib, re, shutil, socket, ssl, subprocess, tempfile, time, urllib.request, urllib.error
repo=pathlib.Path(__file__).resolve().parents[1]
nginx=os.environ.get('NGINX_BIN') or shutil.which('nginx')
if not nginx: raise SystemExit('Set NGINX_BIN to nginx with SSL support. No existing services are changed.')
def port():
    with socket.socket() as s: s.bind(('127.0.0.1',0));return s.getsockname()[1]
with tempfile.TemporaryDirectory(prefix='monitor-nginx-') as tmp:
    root=pathlib.Path(tmp);status,admin,https=port(),port(),port()
    today=datetime.datetime.now(datetime.timezone(datetime.timedelta(hours=8))).isoweekday()
    cfg=(repo/'config.yaml').read_text().replace('active: local','active: cloud')
    cfg=re.sub(r'(?m)^  weekdays:.*$',f'  weekdays: [{(today+1)%7+1}]',cfg)
    (root/'config.yaml').write_text(cfg)
    (root/'trpc_go.yaml').write_text((repo/'trpc_go.yaml').read_text().replace('19028',str(admin)).replace('19029',str(status)))
    (root/'.env').write_text('FEISHU_WEBHOOK=https://open.feishu.cn/open-apis/bot/v2/hook/fake\nFEISHU_WEBHOOK_SECRET=fake\n');(root/'.env').chmod(0o600)
    subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(root/'key.pem'),'-out',str(root/'cert.pem'),'-days','1','-subj','/CN=localhost'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    password=subprocess.check_output(['openssl','passwd','-apr1','fake-password'],text=True).strip()
    (root/'auth').write_text('test:'+password+'\n')
    conf=(repo/'deploy/cloud/nginx/nginx.conf').read_text().replace('/tmp/live-monitor-client',str(root/'client')).replace('/tmp/live-monitor-proxy',str(root/'proxy')).replace('/var/run/nginx.pid',str(root/'nginx.pid')).replace('listen 443 ssl;',f'listen 127.0.0.1:{https} ssl;').replace('/etc/nginx/tls/fullchain.pem',str(root/'cert.pem')).replace('/etc/nginx/tls/privkey.pem',str(root/'key.pem')).replace('/etc/nginx/auth/status.htpasswd',str(root/'auth')).replace('127.0.0.1:19029',f'127.0.0.1:{status}')
    (root/'nginx.conf').write_text(conf);(root/'logs').mkdir()
    subprocess.run([nginx,'-e',str(root/'nginx-error.log'),'-p',str(root)+'/', '-c',str(root/'nginx.conf'),'-t'],check=True)
    env=dict(os.environ,FEISHU_WEBHOOK='https://open.feishu.cn/open-apis/bot/v2/hook/fake',FEISHU_WEBHOOK_SECRET='fake')
    with (root/'process.log').open('w+') as log:
        worker=subprocess.Popen([str(repo/'dist/monitor'),'--root',str(root),'run','--managed','cloud'],env=env,stdout=log,stderr=log)
        proxy=None
        def get(path,auth=False,method='GET'):
            headers={'Authorization':'Basic '+base64.b64encode(b'test:fake-password').decode()} if auth else {}
            req=urllib.request.Request(f'https://127.0.0.1:{https}'+path,headers=headers,method=method)
            try:
                with urllib.request.urlopen(req,context=ssl._create_unverified_context(),timeout=5) as r:return r.status,r.read(),r.headers
            except urllib.error.HTTPError as e:return e.code,e.read(),e.headers
        try:
            for _ in range(100):
                try:
                    with urllib.request.urlopen(f'http://127.0.0.1:{status}/healthz',timeout=.2) as r:
                        if r.status==200:break
                except (OSError,urllib.error.URLError):time.sleep(.1)
            else: raise AssertionError('worker readiness timeout')
            proxy=subprocess.Popen([nginx,'-e',str(root/'nginx-error.log'),'-p',str(root)+'/', '-c',str(root/'nginx.conf'),'-g','daemon off;'],stdout=log,stderr=log)
            for _ in range(100):
                try:
                    if get('/status')[0]==401:break
                except (OSError,urllib.error.URLError):time.sleep(.05)
            else:raise AssertionError('HTTPS readiness timeout')
            code,body,headers=get('/status',True);assert code==200 and json.loads(body)['running'];assert 'no-store' in headers.get('Cache-Control','')
            assert get('/healthz')[0]==401;assert get('/healthz',True)[0]==200
            assert get('/status',True,'POST')[0] in (403,405)
            assert get('/admin',True)[0]==404
            worker.terminate();assert worker.wait(timeout=40)==0
            assert get('/healthz',True)[0] in (502,503)
            print('Actual Nginx TLS + auth + GET-only + uncached status + stopped upstream passed; fake credentials, no real sends.')
        finally:
            if proxy is not None and proxy.poll() is None:proxy.terminate();proxy.wait(timeout=10)
            if worker.poll() is None:worker.terminate();worker.wait(timeout=40)
