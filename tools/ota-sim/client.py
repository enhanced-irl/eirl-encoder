"""The web side of the OTA container test: log in, check, install, wait, report."""
import http.cookiejar, json, sys, time, urllib.request, urllib.error

BASE = "http://127.0.0.1:8080/api/v1"
PASSWORD = "test-password-123"


def opener():
    jar = http.cookiejar.CookieJar()
    o = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    return o


def call(o, method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Content-Type": "application/json", "Accept-Language": "es"})
    try:
        with o.open(req, timeout=10) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, raw.decode()


def login():
    for _ in range(60):
        o = opener()
        try:
            code, _ = call(o, "POST", "/login", {"password": PASSWORD})
            if code == 204:
                return o
        except Exception:
            pass
        time.sleep(1)
    sys.exit("cannot log in")


def wait_state(o, states, timeout=90):
    end = time.time() + timeout
    last = None
    while time.time() < end:
        try:
            code, st = call(o, "GET", "/update")
            if code == 401:
                o = login()
                continue
            last = st
            if code == 200 and st["state"] in states:
                return o, st
        except Exception:
            o = login()
        time.sleep(1)
    sys.exit("timeout waiting for %s, last %s" % (states, json.dumps(last)))


def main():
    action = sys.argv[1]
    o = login()
    if action == "check-install":
        for _ in range(60):
            code, st = call(o, "POST", "/update/check", {})
            if code != 409:
                break
            time.sleep(1)  # busy: the app still follows the helper's last run
        print("check", code, st and st.get("state"))
        o, st = wait_state(o, ["ready", "error"])
        print("before install:", json.dumps({k: st.get(k) for k in ("current", "app", "state", "available", "error", "canInstall")}))
        if st["state"] != "ready":
            sys.exit("not ready")
        code, body = call(o, "POST", "/update/install", {})
        print("install", code, body if code != 202 else body.get("state"))
        if code != 202:
            sys.exit(1)
    elif action == "install-expect-409":
        code, body = call(o, "POST", "/update/check", {})
        o, st = wait_state(o, ["ready", "error"])
        code, body = call(o, "POST", "/update/install", {})
        print("install while streaming:", code, body)
        sys.exit(0 if code == 409 and body.get("error") == "streaming" else 1)
    elif action == "device-writes":
        # The app replaces device.json atomically (temporary file + rename) in /etc/eirl-encoder, which has
        # the sticky bit: twice, so the second rename replaces a file that exists
        for lang in ("fr", "en"):
            code, body = call(o, "PUT", "/device", {"language": lang})
            print("PUT /device language=%s: %s" % (lang, code))
            if code != 200:
                sys.exit("device write failed: %s %s" % (code, body))
    elif action == "status":
        code, st = call(o, "GET", "/update")
        print(json.dumps(st, indent=1))
        code, st = call(o, "GET", "/status")
        print("status.version", json.dumps(st.get("version")), "status.update", json.dumps(st.get("update")))


main()
