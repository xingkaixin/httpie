"""Compare configuration, saved sessions, and download files across implementations."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
from parity import ROOT, Server
import http.server


def main():
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Server)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f'http://127.0.0.1:{server.server_port}'
    outputs = []
    with tempfile.TemporaryDirectory() as temp:
        for label, folder in [('python', '.reference/venv/bin'), ('go', 'bin')]:
            directory = Path(temp, label)
            directory.mkdir()
            env = {k: v for k, v in os.environ.items() if k.lower() not in {'http_proxy', 'https_proxy', 'all_proxy', 'no_proxy'}}
            env['HTTPIE_CONFIG_DIR'] = str(directory)
            config = directory / 'config.json'
            config.write_text('{"disable_update_warnings":true}')
            binary = ROOT / folder / 'http'
            records = []

            def run(args, save=True, command=binary):
                result = subprocess.run([str(command), *args], env=env, input=b'', stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
                if save:
                    records.append((result.returncode, result.stdout))
                elif result.returncode:
                    raise AssertionError(result.stderr.decode())
                return result

            session = directory / 'session.json'
            run(['-I', '--session=' + str(session), url + '/cookies', 'hello=world', 'X-Persist:yes'])
            records.append(json.loads(session.read_text()))
            run(['-I', '--session=' + str(session), url + '/echo'])
            records.append(json.loads(session.read_text()))
            before = session.read_bytes()
            run(['-I', '--session-read-only=' + str(session), url + '/echo', 'X-Persist:no'])
            records.append(before == session.read_bytes())
            run(['-I', '--session=' + str(session), url + '/echo', 'X-Persist:'])
            records.append(json.loads(session.read_text()))
            config.write_text('{"disable_update_warnings":true,"default_options":["--form","--pretty=format"]}')
            run(['-I', '--offline', '--no-form', '--no-pretty', 'example.org', 'a=1'])
            config.write_text('{"disable_update_warnings":true}')
            config.write_text('{broken')
            run(['--help'])
            config.write_text('{"disable_update_warnings":true}')
            destination = directory / 'download'
            destination.write_bytes(b'01234')
            run(['-I', '-dco', str(destination), '-q', url + '/range'])
            records.append(destination.read_bytes())
            for fixture in sorted((ROOT / '.reference/cli-master/tests/fixtures/session_data').rglob('*.json')):
                content = fixture.read_text().replace('__version__', '3.2.4')
                try:
                    json.loads(content)
                except ValueError:
                    continue
                session.write_text(content)
                result = run(['-I', '--offline', '-pH', '--pretty=format', '--session=' + str(session), 'example.org'], save=False)
                records.append((fixture.name, result.stdout, json.loads(session.read_text())))
            session.write_text(json.dumps({'__meta__': {'httpie': '3.0.0'}, 'headers': {'X-Test': 'legacy'},
                                           'cookies': {'test': {'value': 'yes'}}, 'auth': {'type': None}}))
            result = run(['cli', 'sessions', 'upgrade', 'example.org', str(session), '--bind-cookies'], save=False,
                         command=ROOT / folder / 'httpie')
            records.append(json.loads(session.read_text()))
            session.write_text(json.dumps({'__meta__': {'httpie': '3.0.0'}, 'headers': {'X-Z': 'first', 'X-A': 'last'},
                                           'cookies': {'old': {'value': 'expired', 'expires': 1}, 'fresh': {'value': 'live'}},
                                           'auth': {'type': None}}))
            run(['-I', '--offline', '--pretty=none', '--session=' + str(session), 'example.org'])
            records.append(json.loads(session.read_text()))
            env['NETRC'] = str(directory / 'netrc')
            for contents in [
                'machine example.org login user account acct password pass',
                'machine example.org account acct login user password pass',
                r'machine example.org login "user#name" password "pa\ss"',
                r'machine example.org login "user name" password "pa\"ss"',
                'machine example.org login user password pass\nmachine example.org login second password other',
                'machine example.org login user password pass\ninvalid garbage',
                'machine example.org login user password pass\nmacdef foo\nlogin wrong password wrong\n\n',
                'default login fallback password secret',
                'machine example.org user café password pâté',
            ]:
                Path(env['NETRC']).write_text(contents)
                for flags in [[], ['--auth-type=basic'], ['--ignore-netrc']]:
                    run(['-I', '--offline', *flags, 'example.org'])
            session.unlink()
            run(['-I', '--offline', '--session=' + str(session), 'example.org'])
            records.append(json.loads(session.read_text()))
            run(['-I', '--offline', '--session=' + str(session), '-aexplicit:secret', 'example.org'])
            run(['-I', '--offline', '--session=' + str(session), 'example.org'])
            records.append(json.loads(session.read_text()))
            Path(env['NETRC']).unlink()
            manager = ROOT / folder / 'httpie'
            for words in [['cli', 'export-args'], ['cli', 'export-args', '--help'], ['cli', 'check-updates', '--help'],
                          ['cli', 'sessions', '--help'], ['cli', 'sessions', 'upgrade', '--help'], ['cli', 'sessions', 'upgrade-all', '--help']]:
                run(words, command=manager)
            outputs.append(records)
        failures = 0
        for index, (reference, go) in enumerate(zip(*outputs)):
            if reference != go:
                failures += 1
                print('FAIL state', index)
                print('python:', repr(reference)[:1400])
                print('go:    ', repr(go)[:1400])
        server.shutdown()
        print(f'{len(outputs[0]) - failures}/{len(outputs[0])} state comparisons passed')
        if failures:
            raise SystemExit(1)


if __name__ == '__main__':
    main()
