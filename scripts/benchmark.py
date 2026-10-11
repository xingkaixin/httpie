"""Measure complete CLI processes, including startup, on a local HTTP server."""
import http.server
import json
import os
from pathlib import Path
import platform
import re
import statistics
import ssl
import subprocess
import tempfile
import threading
import time

ROOT = Path(__file__).resolve().parents[1]


class Server(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *args):
        pass

    def do_GET(self):
        size = 32 * 1024 * 1024 if self.path == '/large' else 64
        self.send_response_only(200)
        self.send_header('Content-Length', str(size))
        self.end_headers()
        block = b'x' * min(size, 65536)
        for _ in range(size // len(block)):
            self.wfile.write(block)

    def do_POST(self):
        remaining = int(self.headers['Content-Length'])
        while remaining:
            chunk = self.rfile.read(min(65536, remaining))
            if not chunk:
                break
            remaining -= len(chunk)
        self.send_response_only(200)
        self.send_header('Content-Length', '0')
        self.end_headers()


def main():
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Server)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f'http://127.0.0.1:{server.server_port}'
    results = {'platform': platform.platform(), 'machine': platform.machine(),
               'go': subprocess.check_output(['mise', 'exec', '--', 'go', 'version'], text=True).strip(),
               'python': subprocess.check_output([str(ROOT / '.reference/venv/bin/python'), '--version'], text=True).strip(),
               'openssl': subprocess.check_output(['openssl', 'version'], text=True).strip(),
               'samples': 20, 'scenarios': {}}
    with tempfile.TemporaryDirectory() as temp:
        env = {k: v for k, v in os.environ.items() if k.lower() not in {'http_proxy', 'https_proxy', 'all_proxy', 'no_proxy'}}
        env['HTTPIE_CONFIG_DIR'] = temp
        Path(temp, 'config.json').write_text('{"disable_update_warnings":true}')
        upload = Path(temp, 'upload.bin')
        with upload.open('wb') as file:
            for _ in range(512):
                file.write(b'x' * 65536)
        certificate, key = Path(temp, 'cert.pem'), Path(temp, 'key.pem')
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(key),
                        '-out', str(certificate), '-days', '1', '-subj', '/CN=localhost'], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(certificate, key)
        secure_server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Server)
        secure_server.daemon_threads = True
        secure_server.socket = tls.wrap_socket(secure_server.socket, server_side=True)
        threading.Thread(target=secure_server.serve_forever, daemon=True).start()
        https = f'https://127.0.0.1:{secure_server.server_port}'
        scenarios = {
            'version': ['--version'],
            'offline_json': ['-I', '--offline', 'example.org', 'name=HTTPie', 'count:=42'],
            'local_get': ['-I', url],
            'download_32mib': ['-I', '-d', '-q', '-o', os.devnull, url + '/large'],
            'upload_32mib': ['-I', url, '@' + str(upload)],
            'https_get': ['-I', '--verify=no', https],
            'https_download_32mib': ['-I', '--verify=no', '-d', '-q', '-o', os.devnull, https + '/large'],
            'https_upload_32mib': ['-I', '--verify=no', https, '@' + str(upload)],
        }
        for name, args in scenarios.items():
            row = {}
            for label, binary in [('python', ROOT / '.reference/venv/bin/http'), ('go', ROOT / 'bin/http')]:
                command = [str(binary), *args]
                for _ in range(3):
                    subprocess.run(command, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
                timings = []
                for _ in range(results['samples']):
                    start = time.perf_counter_ns()
                    subprocess.run(command, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
                    timings.append((time.perf_counter_ns() - start) / 1_000_000)
                time_args = ['-l'] if platform.system() == 'Darwin' else ['-v']
                measured = subprocess.run(['/usr/bin/time', *time_args, *command], env=env, stdin=subprocess.DEVNULL,
                                          stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True, check=True)
                if platform.system() == 'Darwin':
                    rss = int(re.search(r'(\d+)\s+maximum resident set size', measured.stderr)[1])
                else:
                    rss = int(re.search(r'Maximum resident set size \(kbytes\): (\d+)', measured.stderr)[1]) * 1024
                row[label] = {'median_ms': round(statistics.median(timings), 3),
                              'p95_ms': round(sorted(timings)[int(len(timings) * .95) - 1], 3),
                              'peak_rss_mib': round(rss / 1024 / 1024, 3)}
            row['speedup'] = round(row['python']['median_ms'] / row['go']['median_ms'], 2)
            results['scenarios'][name] = row
            print(name, json.dumps(row), flush=True)
        secure_server.shutdown()
    server.shutdown()
    output = ROOT / '.bench/results.json'
    output.parent.mkdir(exist_ok=True)
    output.write_text(json.dumps(results, indent=2) + '\n')
    print('Saved', output)


if __name__ == '__main__':
    main()
