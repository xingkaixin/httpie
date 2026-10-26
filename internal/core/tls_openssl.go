//go:build cgo

package core

/*
#cgo pkg-config: openssl
#include <openssl/ssl.h>
#include <openssl/err.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    SSL *ssl;
    char error[256];
} httpie_ssl;

static void httpie_error(char *target, size_t length) {
    unsigned long code = ERR_get_error();
    if (code) ERR_error_string_n(code, target, length);
    else snprintf(target, length, "TLS operation failed");
}

static SSL_CTX *httpie_context(int verify, int protocol, const char *ciphers,
                              char *error, size_t error_length) {
    ERR_clear_error();
    SSL_CTX *ctx = SSL_CTX_new(TLS_client_method());
    if (!ctx) goto failed;
    SSL_CTX_set_verify(ctx, verify ? SSL_VERIFY_PEER : SSL_VERIFY_NONE, NULL);
    if (!SSL_CTX_set_min_proto_version(ctx, protocol ? protocol : TLS1_2_VERSION)) goto failed;
    if (protocol && !SSL_CTX_set_max_proto_version(ctx, protocol)) goto failed;
    if (ciphers && *ciphers && !SSL_CTX_set_cipher_list(ctx, ciphers)) goto failed;
    if (verify && !SSL_CTX_set_default_verify_paths(ctx)) goto failed;
    return ctx;
failed:
    httpie_error(error, error_length);
    if (ctx) SSL_CTX_free(ctx);
    return NULL;
}

static int httpie_trust(SSL_CTX *ctx, const char *file, const char *directory, char *error, size_t length) {
    ERR_clear_error();
    int ok = SSL_CTX_load_verify_locations(ctx, *file ? file : NULL, *directory ? directory : NULL);
    if (!ok) httpie_error(error, length);
    return ok;
}

static int httpie_password(char *buffer, int size, int rwflag, void *userdata) {
    const char *password = (const char *)userdata;
    size_t length = strlen(password);
    if (length >= (size_t)size) return 0;
    memcpy(buffer, password, length);
    return (int)length;
}

static int httpie_certificate(SSL_CTX *ctx, const char *cert, const char *key,
                              const char *password, char *error, size_t length) {
    ERR_clear_error();
    SSL_CTX_set_default_passwd_cb(ctx, httpie_password);
    SSL_CTX_set_default_passwd_cb_userdata(ctx, (void *)password);
    int ok = SSL_CTX_use_certificate_chain_file(ctx, cert)
        && SSL_CTX_use_PrivateKey_file(ctx, key, SSL_FILETYPE_PEM)
        && SSL_CTX_check_private_key(ctx);
    SSL_CTX_set_default_passwd_cb_userdata(ctx, NULL);
    if (!ok) httpie_error(error, length);
    return ok;
}

static httpie_ssl *httpie_connection(SSL_CTX *ctx, const char *host, int is_ip) {
    httpie_ssl *connection = calloc(1, sizeof(httpie_ssl));
    if (!connection) return NULL;
    connection->ssl = SSL_new(ctx);
    if (!connection->ssl) { free(connection); return NULL; }
    BIO *input = BIO_new(BIO_s_mem());
    BIO *output = BIO_new(BIO_s_mem());
    if (!input || !output) {
        BIO_free(input); BIO_free(output); SSL_free(connection->ssl); free(connection); return NULL;
    }
    SSL_set_bio(connection->ssl, input, output);
    SSL_set_connect_state(connection->ssl);
    if (!is_ip) SSL_set_tlsext_host_name(connection->ssl, host);
    if (is_ip) X509_VERIFY_PARAM_set1_ip_asc(SSL_get0_param(connection->ssl), host);
    else X509_VERIFY_PARAM_set1_host(SSL_get0_param(connection->ssl), host, 0);
    return connection;
}

static int httpie_step(httpie_ssl *connection, int operation, void *data, int length, int *result) {
    ERR_clear_error();
    int value;
    if (operation == 0) value = SSL_do_handshake(connection->ssl);
    else if (operation == 1) value = SSL_read(connection->ssl, data, length);
    else value = SSL_write(connection->ssl, data, length);
    *result = value;
    if (value > 0) return SSL_ERROR_NONE;
    int error = SSL_get_error(connection->ssl, value);
    if (error != SSL_ERROR_WANT_READ && error != SSL_ERROR_WANT_WRITE && error != SSL_ERROR_ZERO_RETURN)
        httpie_error(connection->error, sizeof(connection->error));
    return error;
}

static int httpie_drain(httpie_ssl *connection, void *data, int length) {
    return BIO_read(SSL_get_wbio(connection->ssl), data, length);
}
static void httpie_feed(httpie_ssl *connection, void *data, int length) {
    BIO_write(SSL_get_rbio(connection->ssl), data, length);
}
static void httpie_free(httpie_ssl *connection) {
    SSL_free(connection->ssl);
    free(connection);
}
*/
import "C"

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"unsafe"
)

type tlsSettings struct {
	context *C.SSL_CTX
	options Options
}

func newTLSSettings(o Options) (*tlsSettings, error) {
	verify := strings.ToLower(o.Value("verify"))
	check := verify != "no" && verify != "false"
	for _, name := range []string{"cert", "cert-key"} {
		if o.Has(name) {
			if _, err := os.Stat(ExpandPath(o.Value(name))); err != nil {
				return nil, err
			}
		}
	}
	protocols := map[string]int{"": 0, "ssl2.3": 0, "tls1": 0x301, "tls1.1": 0x302, "tls1.2": 0x303, "tls1.3": 0x304}
	protocol, ok := protocols[o.Value("ssl")]
	if !ok {
		return nil, fmt.Errorf("unsupported SSL version: %s", o.Value("ssl"))
	}
	ciphers := C.CString(o.Value("ciphers"))
	defer C.free(unsafe.Pointer(ciphers))
	var message [256]C.char
	verifyFlag := 0
	if check {
		verifyFlag = 1
	}
	ctx := C.httpie_context(C.int(verifyFlag), C.int(protocol), ciphers, &message[0], C.size_t(len(message)))
	if ctx == nil {
		return nil, fmt.Errorf("OpenSSL: %s", C.GoString(&message[0]))
	}
	return &tlsSettings{context: ctx, options: o}, nil
}
func (settings *tlsSettings) configure() error {
	o := settings.options
	ctx := settings.context
	var message [256]C.char
	verify := strings.ToLower(o.Value("verify"))
	check := verify != "no" && verify != "false"
	caFile, caDir := "", ""
	if check {
		if verify == "yes" || verify == "true" {
			caFile = os.Getenv("REQUESTS_CA_BUNDLE")
			if caFile == "" {
				caFile = os.Getenv("CURL_CA_BUNDLE")
			}
		} else {
			caFile = o.Value("verify")
		}
		caFile = ExpandPath(caFile)
		if stat, err := os.Stat(caFile); err == nil && stat.IsDir() {
			caDir = caFile
			caFile = ""
		}
	}

	if caFile != "" || caDir != "" {
		file := C.CString(caFile)
		defer C.free(unsafe.Pointer(file))
		directory := C.CString(caDir)
		defer C.free(unsafe.Pointer(directory))
		if C.httpie_trust(ctx, file, directory, &message[0], C.size_t(len(message))) != 1 {
			return fmt.Errorf("OpenSSL: %s", C.GoString(&message[0]))
		}
	}

	if o.Has("cert") {
		keyPath := o.Value("cert-key")
		if keyPath == "" {
			keyPath = o.Value("cert")
		}
		password := o.Value("cert-key-pass")
		if !o.Has("cert-key-pass") {
			data, err := os.ReadFile(ExpandPath(keyPath))
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte("ENCRYPTED")) {
				password, err = readPassword("Password for client private key: ")
				if err != nil {
					return err
				}
			}
		}
		cert := C.CString(ExpandPath(o.Value("cert")))
		defer C.free(unsafe.Pointer(cert))
		key := C.CString(ExpandPath(keyPath))
		defer C.free(unsafe.Pointer(key))
		pass := C.CString(password)
		defer C.free(unsafe.Pointer(pass))
		if C.httpie_certificate(ctx, cert, key, pass, &message[0], C.size_t(len(message))) != 1 {
			return fmt.Errorf("OpenSSL: %s", C.GoString(&message[0]))
		}
	}
	return nil
}
func (s *tlsSettings) close() {
	if s.context != nil {
		C.SSL_CTX_free(s.context)
		s.context = nil
	}
}
func (s *tlsSettings) connect(ctx context.Context, conn net.Conn, host string) (net.Conn, error) {
	if err := s.configure(); err != nil {
		return nil, err
	}
	host = strings.TrimSuffix(host, ".")
	hostname := C.CString(host)
	defer C.free(unsafe.Pointer(hostname))
	isIP := 0
	if net.ParseIP(host) != nil {
		isIP = 1
	}
	state := C.httpie_connection(s.context, hostname, C.int(isIP))
	if state == nil {
		return nil, fmt.Errorf("cannot create OpenSSL connection")
	}
	secured := &opensslConn{Conn: conn, state: state}
	stop := context.AfterFunc(ctx, func() { secured.Close() })
	defer stop()
	if _, err := secured.operate(0, nil); err != nil {
		secured.Close()
		return nil, err
	}
	return secured, nil
}

type opensslConn struct {
	net.Conn
	mutex sync.Mutex
	state *C.httpie_ssl
}

func (c *opensslConn) Close() error {
	err := c.Conn.Close()
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.state != nil {
		C.httpie_free(c.state)
		c.state = nil
	}
	return err
}
func (c *opensslConn) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return c.operate(1, data)
}
func (c *opensslConn) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return c.operate(2, data)
}
func (c *opensslConn) operate(operation int, data []byte) (int, error) {
	var pointer unsafe.Pointer
	if len(data) > 0 {
		pointer = unsafe.Pointer(&data[0])
	}
	buffer := make([]byte, 32*1024)
	for {
		c.mutex.Lock()
		if c.state == nil {
			c.mutex.Unlock()
			return 0, net.ErrClosed
		}
		var result C.int
		status := C.httpie_step(c.state, C.int(operation), pointer, C.int(len(data)), &result)
		message := C.GoString(&c.state.error[0])
		var output []byte
		for {
			n := int(C.httpie_drain(c.state, unsafe.Pointer(&buffer[0]), C.int(len(buffer))))
			if n <= 0 {
				break
			}
			output = append(output, buffer[:n]...)
		}
		c.mutex.Unlock()
		if len(output) > 0 {
			if _, err := io.Copy(c.Conn, bytes.NewReader(output)); err != nil {
				return 0, err
			}
		}
		switch status {
		case C.SSL_ERROR_NONE:
			return int(result), nil
		case C.SSL_ERROR_ZERO_RETURN:
			return 0, io.EOF
		case C.SSL_ERROR_WANT_WRITE:
			continue
		case C.SSL_ERROR_WANT_READ:
			n, err := c.Conn.Read(buffer)
			if n > 0 {
				c.mutex.Lock()
				if c.state == nil {
					c.mutex.Unlock()
					return 0, net.ErrClosed
				}
				C.httpie_feed(c.state, unsafe.Pointer(&buffer[0]), C.int(n))
				c.mutex.Unlock()
			}
			if err != nil {
				return 0, err
			}
		default:
			return 0, fmt.Errorf("OpenSSL: %s", message)
		}
	}
}
