"""Compare observable CLI behavior with the supplied HTTPie release."""
import ast
import difflib
import gzip
import hashlib
import http.server
import json
import os
import errno
import pty
import tty
from pathlib import Path
import subprocess
import tempfile
import threading
import time
import zlib

ROOT = Path(__file__).resolve().parents[1]
REFERENCE = ROOT / '.reference/venv/bin/http'
GO = ROOT / 'bin/http'


class Server(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    responses = {}

    def log_message(self, *args):
        pass

    def handle(self):
        try:
            super().handle()
        except (BrokenPipeError, ConnectionResetError):
            pass

    def do_GET(self):
        if self.path == '/folded':
            self.wfile.write(b'HTTP/1.1 200 OK\r\nX-Folded: first\r\n second\r\nContent-Length: 0\r\n\r\n')
            return
        body = b''
        if self.headers.get('Transfer-Encoding') == 'chunked':
            while True:
                size = int(self.rfile.readline().strip(), 16)
                if not size:
                    self.rfile.readline()
                    break
                body += self.rfile.read(size)
                self.rfile.read(2)
        else:
            body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        if self.headers.get('Content-Encoding') == 'deflate':
            body = zlib.decompress(body)
        status, headers, content_type = 200, {}, 'application/json'
        payload = json.dumps({
            'method': self.command,
            'path': self.path,
            'headers': {k.lower(): self.headers.get_all(k) for k in self.headers.keys()
                        if k.lower() not in {'content-length', 'content-encoding'}},
            'body': body.decode('utf-8', errors='replace')
        }, sort_keys=True).encode()
        if self.path in self.responses:
            response = self.responses[self.path]
            payload, content_type, headers = response[:3]
            if len(response) > 3:
                status = response[3]
        elif self.path.startswith('/status/'):
            status = int(self.path.split('/')[-1])
            payload = b'status'
        elif self.path.startswith('/redirect/'):
            status = int(self.path.split('/')[-1])
            headers['Location'] = '/echo'
            payload = b'redirect'
        elif self.path == '/loop':
            status = 302
            headers['Location'] = '/loop'
        elif self.path == '/digest':
            payload = b'ok'
            content_type = 'text/plain'
            if not self.headers.get('Authorization', '').startswith('Digest '):
                status = 401
                headers['WWW-Authenticate'] = 'Digest realm="test", nonce="fixed", qop="auth", algorithm=MD5'
                payload = b'challenge'
        elif self.path.startswith('/set-cookie'):
            headers['Set-Cookie'] = self.headers.get('X-Set-Cookie', '')
        elif self.path == '/cookies':
            headers['Set-Cookie'] = 'token=secret; Path=/'
        elif self.path == '/json':
            payload = b'{"z":1,"a":[true,null,"hello"],"z":2}'
        elif self.path == '/xml':
            payload = b'<root><a>Hello</a><empty/></root>'
            content_type = 'application/xml'
        elif self.path == '/binary':
            payload = bytes(range(256))
            content_type = 'application/octet-stream'
        elif self.path.startswith('/encoded/'):
            _, _, encoding, declared = self.path.split('/')
            text = '卷首' * 25 if encoding == 'big5' else 'Všichni lidé jsou si rovni. ' * 2
            payload = text.encode(encoding)
            content_type = 'text/plain' + ('; charset=' + declared if declared != 'guess' else '')
        elif self.path == '/latin1':
            payload = 'caf\xe9'.encode('latin1')
            content_type = 'text/plain; charset=iso-8859-1'
        elif self.path == '/slow':
            time.sleep(.25)
        elif self.path == '/range':
            payload = b'0123456789' * 1024
            if self.headers.get('Range'):
                offset = int(self.headers['Range'].split('=')[1].split('-')[0])
                headers['Content-Range'] = f'bytes {offset}-{len(payload)-1}/{len(payload)}'
                status = 206
                payload = payload[offset:]
        elif self.path.startswith('/compressed/'):
            payload = b'{"message":"compressed"}'
            encoding = self.path.split('/')[-1]
            if encoding == 'gzip':
                payload = gzip.compress(payload)
            elif encoding == 'raw':
                payload = zlib.compress(payload)[2:-4]
                encoding = 'deflate'
            else:
                payload = zlib.compress(payload)
            headers['Content-Encoding'] = encoding
        self.send_response_only(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(payload)))
        for key, value in headers.items():
            self.send_header(key, value)
        self.end_headers()
        if self.command != 'HEAD':
            try:
                self.wfile.write(payload)
            except (BrokenPipeError, ConnectionResetError):
                pass

    do_POST = do_PUT = do_PATCH = do_DELETE = do_OPTIONS = do_HEAD = do_GET


def invoke(binary, args, stdin, env, terminal=False):
    if not terminal:
        result = subprocess.run([str(binary), *args], input=stdin or b'', stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, env=env, timeout=10)
        return result.returncode, result.stdout
    master, slave = pty.openpty()
    tty.setraw(slave)
    process = subprocess.Popen([str(binary), *args], stdin=subprocess.DEVNULL, stdout=slave,
                               stderr=subprocess.PIPE, env=env)
    os.close(slave)
    output = bytearray()
    try:
        while True:
            try:
                chunk = os.read(master, 65536)
            except OSError as error:
                if error.errno == errno.EIO:
                    break
                raise
            if not chunk:
                break
            output.extend(chunk)
    finally:
        os.close(master)
    process.communicate(timeout=10)
    return process.returncode, bytes(output)


def main():
    if not REFERENCE.exists():
        raise SystemExit('Run scripts/setup-reference.sh first')
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Server)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f'http://127.0.0.1:{server.server_port}'
    cases = []

    def add(name, args, stdin=None, terminal=False, env=None):
        cases.append((name, args, stdin, terminal, env or {}))

    add('version', ['--version'])
    add('help', ['--help'])
    add('manual', ['--manual'])
    for width in [20, 40, 80, 120]:
        add(f'manual fallback width {width}', ['--manual'], env={'HTTPIE_NO_MAN_PAGES': '1', 'COLUMNS': str(width)})
        add(f'help width {width}', ['--help'], env={'COLUMNS': str(width)})
    for suffix in ['/a%2Fb', '/a//b', '/a/./b/../c/', '/a/%2E%2E/b', '/a%20b?q=a b', '/café?q=中文']:
        for flag in [[], ['--path-as-is']]:
            add('URL ' + suffix + str(flag), ['-I', '--offline', *flag, 'example.org' + suffix])
    palettes = json.loads((ROOT / 'internal/cli/palettes.json').read_text())
    for style in palettes:
        if not style.endswith('-headers'):
            add('color ' + style, ['-I', '--offline', '--pretty=all', '--style=' + style,
                                  'example.org', 'z:=15', 'x=yes', 'bool:=true'])
    for flags in [[], ['--pretty=none'], ['--pretty=format'], ['--unsorted'], ['-pH'], ['-pB']]:
        add('terminal ' + str(flags), ['-I', '--offline', *flags, 'example.org', 'name=value'], terminal=True)
    for fixture in sorted((ROOT / '.reference/cli-master/tests/fixtures/xmldata').rglob('*_raw.xml')):
        add('XML fixture ' + fixture.name, ['-I', '--offline', '--pretty=format', '-pB',
                                          'example.org', '@' + str(fixture)])

    for args in [[], ['-j'], ['-f'], ['--multipart', '--boundary=BOUNDARY'],
                 ['-v'], ['-vv', '-pHB'], ['--pretty=format'], ['--raw=raw body'],
                 ['-auser:pass'], ['-A', 'bearer', '-a', 'token'],
                 ['--chunked', '--raw=hello'], ['--print=H'], ['--print=B'],
                 ['--form', '--no-json'], ['--json', '--no-form'],
                 ['--default-scheme=https'], ['--no-offline']]:
        if '--no-offline' in args:
            continue
        add('offline ' + ' '.join(args), ['--offline', '-I', *args, 'example.org'])
    for items in [[], ['a=1', 'b:=true'], ['z=last', 'a=first'], ['a=1', 'a=2'],
                  ['one==a b', 'one==c'], ['X-Test:foo', 'X-Test:bar'], ['X-Empty;'],
                  ['User-Agent:', 'Accept:', 'Connection:'], ['hello=世界'],
                  [r'key\:name=value'], ['[2][a]=foo', '[]=bar'],
                  ['a[0]=x', 'a[]=y'], ['[]=one', '[]=two']]:
        for flags in [[], ['--pretty=format'], ['--pretty=format', '--unsorted']]:
            add('offline items ' + str(items) + str(flags), ['-I', '--offline', *flags, 'example.org', *items])
    for items in [[r'a=b\=c', r'x==q\=2'], [r'path=c:\\windows', r'esc\\:header'],
                  ['X-Test:one', 'X-Test:', 'X-Test:two'], ['X-Test:one', 'X-Other:yes', 'X-Test:'],
                  ['a==1', 'b==2', 'a==3'], ['a=1', 'b=2', 'a=3']]:
        for flags in [[], ['-f']]:
            add('request item edge ' + repr((flags, items)), ['-I', '--offline', *flags, 'example.org', *items])
    for content_type in ['multipart/magic', 'multipart/magic; boundary=HEADER_BOUNDARY', '']:
        add('multipart content type ' + content_type, ['-I', '--offline', '--multipart', '--boundary=BODY_BOUNDARY',
            'example.org', 'a=1', 'b=2', 'a=3', 'Content-Type:' + content_type])
    for number in ['1e3', '1e16', '1e-5', '1.23456789123456789', '-0.0', '-0', 'NaN', 'Infinity', '-Infinity', '1e999']:
        for flags in [[], ['--pretty=format'], ['-f']]:
            add('JSON number ' + number + str(flags), ['-I', '--offline', *flags, 'example.org', 'n:=' + number])
    for credentials in [r'u:p\:x', r'u\:x:p', r'u\\:p', r'u:p\\x', r'u:p\@x', ':', '', r'u\:p']:
        add('escaped auth ' + repr(credentials), ['-I', '--offline', '-a', credentials, 'example.org'])
    for target in ['http://u%3Ax:p%40x@example.org', 'http://u:p%2fx@example.org']:
        add('encoded URL auth ' + target, ['-I', '--offline', target])
    response_cases = [
        (b'', 'application/json', {}),
        (b'a\rb\r\n\nc', 'text/plain', {}),
        (b'a\n\n', 'text/plain', {}),
        (b'\n\n{"a":1}\n\n', 'application/json', {}),
        (b'hello', 'foo/bar', {}),
        (b'hello', 'application/xml', {}),
        (b'<root a="1">hi &amp; world<child /></root>', 'application/xml', {}),
        (b'\x00', 'application/octet-stream', {}),
        (b'', 'application/json', {'Content-Encoding': 'gzip'}),
        (b'', 'application/json', {'Content-Encoding': 'deflate'}),
    ]
    for index, response in enumerate(response_cases):
        endpoint = '/output-case/' + str(index)
        Server.responses[endpoint] = response
        for terminal in [False, True]:
            for pretty in ['none', 'format', 'colors']:
                for stream in [[], ['--stream']]:
                    add('output edge ' + repr((index, terminal, pretty, stream)),
                        ['-I', '-b', '--pretty=' + pretty, *stream, url + endpoint], terminal=terminal)
    for flags in [['--pretty=format'], ['--pretty=format', '--stream'], ['--pretty=none']]:
        add('binary headers ' + repr(flags), ['-I', '-pHBhb', *flags, url + '/binary'], terminal=True)
    for mime in ['', 'foo/bar', 'application/octet-stream', 'application/xml', 'application/json', 'text/plain']:
        for payload in ['hello', 'hello\rworld\r\nnext\n', 'line\n\n', '', '{"z":1,"a":2}']:
            add('raw coloring ' + repr((mime, payload)), ['-I', '--offline', '-pB', '--pretty=colors',
                '--raw=' + payload, 'example.org', 'Content-Type:' + mime])
    for flags in [
        ['--raw', '--help'], ['--timeout', '--version'], ['--timeout=-1'], ['--timeout=nan'],
        ['--max-redirects=-1'], ['--max-headers=-1'], ['--pretty=bad', '--help'], ['--style=bad', '--help'],
        ['--auth-type=bad', '--help'], ['--ssl=bad', '--help'], ['--response-charset=bad', '--help'],
        ['--response-mime=too/many/slashes'], ['--format-options=json.indent:+2'], ['--session='],
        ['--raw=-x'], ['--raw', '-3'], ['-a=abc:def'], ['-p=H'], ['--style', '--help'],
        ['--auth-type', '--help'], ['--session=a', '--no-session', '--session-read-only=b'],
    ]:
        add('argument validation ' + repr(flags), ['-I', '--offline', *flags, 'example.org'])
    for flags in [['--timeout=-1'], ['--timeout=nan'], ['--timeout=inf'], ['--max-headers=-1']]:
        add('network argument validation ' + repr(flags), ['-I', *flags, url + '/echo'])
    add('explicit Authorization with auth', ['-I', '--offline', '-auser:pass', 'example.org', 'Authorization:old', 'X-Test:yes'])
    add('JSON input duplicate keys', ['-I', '--offline', 'example.org', 'a:={"x":1,"x":2}'])
    for method in ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']:
        add(method, ['-I', method, url + '/echo'])
    for flags, items in [([], []), ([], ['a=1', 'b:=42']), (['-f'], ['a=1', 'a=2']),
                         (['--multipart', '--boundary=BOUNDARY'], ['a=1', 'a=2']),
                         (['-x'], ['large=' + 'x' * 1024]), (['-xx'], ['a=b']),
                         (['--chunked'], ['a=b']), (['--raw=raw data'], []),
                         (['-auser:pass'], []), (['-A', 'bearer', '-a', 'secret'], []),
                         ([], ['X-Test:one', 'X-Test:two']), ([], ['X-Empty;']),
                         ([], ['q==hello world', 'q==中文'])]:
        add('network ' + str(flags) + str(items)[:70], ['-I', *flags, url + '/echo', *items])
    for endpoint in ['json', 'xml', 'binary', 'latin1', 'compressed/gzip', 'compressed/deflate', 'compressed/raw']:
        for flags in [[], ['-h'], ['-b'], ['-pHBhb'], ['--pretty=format'], ['--stream'], ['--pretty=format', '--stream']]:
            add(endpoint + str(flags), ['-I', *flags, url + '/' + endpoint])
    for encoding in ['big5', 'windows-1250', 'utf-8']:
        for declared in [encoding, 'guess', 'utf-8']:
            for pretty in ['none', 'format', 'all']:
                flags = ['--response-charset=' + encoding] if declared == 'utf-8' else []
                add('charset ' + str((encoding, declared, pretty)), ['-I', '-b', '--pretty=' + pretty,
                    *flags, url + '/encoded/' + encoding + '/' + declared], terminal=True)
    for status in [200, 301, 400, 404, 500, 503]:
        for flags in [[], ['--check-status'], ['--check-status', '-q'], ['--check-status', '-qq']]:
            add('status ' + str(status) + str(flags), ['-I', *flags, url + '/status/' + str(status)])
    for status in [301, 302, 303, 307, 308]:
        for flags in [[], ['-F'], ['-Fv'], ['-F', '--all', '--history-print=h']]:
            add('redirect ' + str(status) + str(flags), ['-I', *flags, url + '/redirect/' + str(status), 'a=1'])
    for flags in [['-v'], ['-pH'], ['-pB'], ['-pHBhb'], ['--all', '-pH'], ['--all', '-pB']]:
        add('redirect request output ' + repr(flags), ['-I', '-F', *flags, url + '/redirect/302', 'data=yes'])
    for flags in [[], ['-v'], ['-pH'], ['--all'], ['--all', '-pHBhb']]:
        add('digest handshake output ' + repr(flags), ['-I', '-Adigest', '-au:p', *flags, url + '/digest'])
    add('redirect custom Host', ['-I', '-F', url + '/redirect/302', 'Host:custom.example'])
    add('redirect deleted Host', ['-I', '-F', '-pH', url + '/redirect/302', 'Host:'])
    add('redirect limit without follow', ['-I', '--max-redirects=1', url + '/redirect/302'])
    add('failed connection request output', ['-I', '-v', '--timeout=.2', 'http://127.0.0.1:1'])
    dead_proxy = 'http://127.0.0.1:1'
    for label, flags, environment in [
        ('lowercase wins', [], {'HTTP_PROXY': url, 'http_proxy': dead_proxy}),
        ('empty lowercase disables uppercase', [], {'HTTP_PROXY': dead_proxy, 'http_proxy': ''}),
        ('CGI ignores uppercase HTTP_PROXY', [], {'HTTP_PROXY': dead_proxy, 'REQUEST_METHOD': 'GET'}),
        ('scheme before all', ['--proxy=http:' + url, '--proxy=all:' + dead_proxy], {}),
        ('environment scheme before explicit all', ['--proxy=all:' + dead_proxy], {'http_proxy': url}),
        ('host before scheme', ['--proxy=http\\://127.0.0.1:' + url, '--proxy=http:' + dead_proxy], {}),
        ('NO_PROXY domain', [], {'http_proxy': dead_proxy, 'no_proxy': '127.0.0.1'}),
        ('NO_PROXY IPv4 with port', [], {'http_proxy': dead_proxy, 'no_proxy': '127.0.0.1:' + str(server.server_port)}),
        ('NO_PROXY CIDR', [], {'http_proxy': dead_proxy, 'no_proxy': '127.0.0.0/8'}),
        ('explicit proxy ignores NO_PROXY', ['--proxy=http:' + dead_proxy], {'no_proxy': '*'}),
    ]:
        add('proxy precedence ' + label, ['-I', '--timeout=.5', *flags, url + '/echo'], env=environment)
    add('folded response headers', ['-I', '-h', url + '/folded'])
    add('folded header count exact', ['-I', '--max-headers=4', url + '/folded'])
    add('folded header count too small', ['-I', '--max-headers=3', url + '/folded'])
    add('offline unused CA', ['-I', '--offline', '--verify=/nonexistent/ca.pem', 'example.org'])
    add('HTTP unused CA', ['-I', '--verify=/nonexistent/ca.pem', url + '/echo'])
    add('redirect limit', ['-I', '-F', '--max-redirects=2', url + '/loop'])
    add('timeout', ['-I', '--timeout=.05', url + '/slow'])
    add('max headers', ['-I', '--max-headers=1', url + '/echo'])
    add('stdin', [url + '/echo'], b'input\x00bytes')
    add('empty stdin method', [url + '/echo'], b'')
    add('stdin conflict', [url + '/echo', 'a=1'], b'input')
    for args in [['--invalid'], ['-I'], ['--pretty=invalid', 'example.org'],
                 ['-I', '--continue', 'example.org'], ['-I', '--compress', '--chunked', 'example.org']]:
        add('invalid ' + str(args), args)
    with tempfile.TemporaryDirectory() as temp:
        env = {k: v for k, v in os.environ.items() if k.lower() not in {'http_proxy', 'https_proxy', 'all_proxy', 'no_proxy'}}
        env['HTTPIE_CONFIG_DIR'] = temp
        env['TERM'] = 'dumb'
        Path(temp, 'config.json').write_text('{"disable_update_warnings":true}')
        fixture = Path(temp, 'data.txt')
        fixture.write_text('hello file\n')
        man = Path(temp, 'man')
        man.write_text('#!/bin/sh\nprintf "manual section=%s program=%s\\n" "$1" "$2"\n')
        man.chmod(0o755)
        add('manual system command', ['--manual'], env={'PATH': temp + os.pathsep + env['PATH'], 'HTTPIE_NO_MAN_PAGES': ''})
        for flags, items in [([], ['@' + str(fixture)]), (['-f', '--boundary=BOUNDARY'], ['f@' + str(fixture), 'a=b']),
                             ([], ['text=@' + str(fixture)]), ([], ['q==@' + str(fixture)]),
                             ([], ['X-File:@' + str(fixture)])]:
            add('file ' + str(flags) + str(items), ['-I', *flags, url + '/echo', *items])
        source = ast.parse((ROOT / '.reference/cli-master/tests/test_json.py').read_text())
        for node in ast.walk(source):
            if isinstance(node, ast.FunctionDef) and node.name == 'test_nested_json_syntax':
                for decorator in node.decorator_list:
                    if isinstance(decorator, ast.Call) and len(decorator.args) > 1:
                        for index, entry in enumerate(decorator.args[1].elts):
                            try:
                                inputs, expected = ast.literal_eval(entry)
                            except (ValueError, TypeError):
                                continue
                            add(f'upstream nested JSON {index}', ['-I', url + '/echo', *inputs])
        failures = []
        selected = [case for case in cases if os.getenv('PARITY_FILTER', '') in case[0]]
        for name, args, stdin, terminal, overrides in selected:
            outputs = []
            case_env = dict(env, TERM='xterm-256color') if name.startswith('color ') or terminal else env
            case_env = dict(case_env, **overrides)
            for binary in [REFERENCE, GO]:
                outputs.append(invoke(binary, args, stdin, case_env, terminal))
            if outputs[0] != outputs[1]:
                failures.append(name)
                print('FAIL:', name)
                print('exit:', outputs[0][0], outputs[1][0])
                for line in list(difflib.unified_diff(outputs[0][1].decode(errors='replace').splitlines(),
                                                     outputs[1][1].decode(errors='replace').splitlines(),
                                                     fromfile='python', tofile='go'))[:30]:
                    print(line)
        server.shutdown()
        print(f'{len(selected) - len(failures)}/{len(selected)} cases passed')
        if failures:
            raise SystemExit(1)


if __name__ == '__main__':
    main()
