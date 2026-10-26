//go:build darwin

package instalacao

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSystem struct {
	t        *testing.T
	calls    []string
	signed   []string
	keychain string

	bootstrapErr []error // consumed per Bootstrap call
}

func (f *fakeSystem) ops() Operations {
	return Operations{
		RunCommand: func(name string, args ...string) ([]byte, error) {
			f.t.Fatalf("unexpected real command in Install test: %s %v", name, args)
			return nil, nil
		},
		SignBundle: func(bundle, keychain string) error {
			f.calls = append(f.calls, "sign")
			f.signed = append(f.signed, bundle)
			f.keychain = keychain
			if _, err := os.Stat(filepath.Join(bundle, "Contents", "MacOS", ExecutableName)); err != nil {
				f.t.Fatalf("signing an incomplete bundle: %v", err)
			}
			return nil
		},
		Bootout: func(uid, label string) error {
			f.calls = append(f.calls, "bootout gui/"+uid+"/"+label)
			return nil
		},
		Bootstrap: func(uid, plist string) error {
			f.calls = append(f.calls, "bootstrap gui/"+uid+" "+filepath.Base(plist))
			if len(f.bootstrapErr) > 0 {
				err := f.bootstrapErr[0]
				f.bootstrapErr = f.bootstrapErr[1:]
				return err
			}
			return nil
		},
		Logf: func(string, ...any) {},
	}
}

func testDarwinDeps(t *testing.T) (Deps, *fakeSystem, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "barroso-wispr-darwin-arm64")
	if err := os.WriteFile(source, []byte("new-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{t: t}
	return Deps{
		Source:        func() (string, error) { return source, nil },
		Home:          func() (string, error) { return home, nil },
		CurrentUID:    func() (string, error) { return "501", nil },
		APIKey:        func() string { return "bk-secret&<key>" },
		RunningAsRoot: func() bool { return false },
		Ops:           f.ops(),
	}, f, home
}

func paths(home string) (bundle, exe, plist string) {
	bundle = filepath.Join(home, "Applications", BundleName)
	return bundle, filepath.Join(bundle, "Contents", "MacOS", ExecutableName),
		filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
}

func writeExisting(t *testing.T, home string) {
	t.Helper()
	_, exe, plist := paths(home)
	for path, data := range map[string]string{exe: "old-binary", plist: "old-plist"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertNoStaging(t *testing.T, home string) {
	t.Helper()
	for _, pattern := range []string{
		filepath.Join(home, "Applications", ".barroso-wispr-install-*"),
		filepath.Join(home, "Library", "LaunchAgents", ".tec.barroso.wispr-install-*"),
	} {
		if m, _ := filepath.Glob(pattern); len(m) != 0 {
			t.Fatalf("staging/backup left behind: %v", m)
		}
	}
}

func equalCalls(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestInstallRefusesRootBeforeAnyWork(t *testing.T) {
	work := 0
	deps := Deps{
		RunningAsRoot: func() bool { return true },
		Source:        func() (string, error) { work++; return "", nil },
		Home:          func() (string, error) { work++; return "", nil },
		Ops:           Operations{MkdirAll: func(string, os.FileMode) error { work++; return nil }},
	}
	if err := Install(deps); !errors.Is(err, ErrSudoNotAllowed) {
		t.Fatalf("Install = %v", err)
	}
	if work != 0 {
		t.Fatalf("root install did %d operations", work)
	}
}

func TestInstallRequiresAPIKeyBeforeMutating(t *testing.T) {
	deps, f, home := testDarwinDeps(t)
	deps.APIKey = func() string { return "  " }
	if err := Install(deps); !errors.Is(err, ErrAPIKeyRequired) {
		t.Fatalf("Install = %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls = %v", f.calls)
	}
	if _, err := os.Stat(filepath.Join(home, "Applications")); !os.IsNotExist(err) {
		t.Fatalf("Applications created: %v", err)
	}
}

func TestInstallFreshBuildsSignedBundleAndBootstrapsAgent(t *testing.T) {
	deps, f, home := testDarwinDeps(t)
	if err := Install(deps); err != nil {
		t.Fatal(err)
	}
	bundle, exe, plist := paths(home)
	equalCalls(t, f.calls, []string{
		"sign",
		"bootout gui/501/" + LaunchAgentLabel,
		"bootstrap gui/501 " + LaunchAgentLabel + ".plist",
	})
	if dir := filepath.Dir(f.signed[0]); dir != filepath.Join(home, "Applications") {
		t.Fatalf("bundle not staged beside target: %s", f.signed[0])
	}
	if want := filepath.Join(home, "Library", "Keychains", "login.keychain-db"); f.keychain != want {
		t.Fatalf("keychain = %q, want %q", f.keychain, want)
	}
	if got := readFile(t, exe); got != "new-binary" {
		t.Fatalf("executable = %q", got)
	}
	if st, _ := os.Stat(exe); st.Mode().Perm() != 0o755 {
		t.Fatalf("executable mode = %v", st.Mode())
	}
	info := readFile(t, filepath.Join(bundle, "Contents", "Info.plist"))
	for _, want := range []string{"<string>" + BundleIdentifier + "</string>", "<key>LSUIElement</key><true/>", "NSMicrophoneUsageDescription", "<string>" + ExecutableName + "</string>"} {
		if !strings.Contains(info, want) {
			t.Errorf("Info.plist missing %q", want)
		}
	}
	st, err := os.Stat(plist)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("plist mode = %v, %v", st, err)
	}
	agent := readFile(t, plist)
	for _, want := range []string{"<string>" + exe + "</string>", "<key>WISPR_API_KEY</key><string>bk-secret&amp;&lt;key&gt;</string>", "<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><false/>"} {
		if !strings.Contains(agent, want) {
			t.Errorf("plist missing %q:\n%s", want, agent)
		}
	}
	assertNoStaging(t, home)
}

func TestInstallUpgradeReplacesAndRemovesBackups(t *testing.T) {
	deps, _, home := testDarwinDeps(t)
	writeExisting(t, home)
	_, exe, plist := paths(home)
	if err := Install(deps); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, exe); got != "new-binary" {
		t.Fatalf("executable = %q", got)
	}
	if strings.Contains(readFile(t, plist), "old-plist") {
		t.Fatal("plist not replaced")
	}
	assertNoStaging(t, home)
}

func TestInstallUpgradeRollsBackWhenBootstrapFails(t *testing.T) {
	deps, f, home := testDarwinDeps(t)
	writeExisting(t, home)
	f.bootstrapErr = []error{errors.New("bootstrap failed")}
	err := Install(deps)
	if err == nil || !strings.Contains(err.Error(), "registrar o LaunchAgent") || strings.Contains(err.Error(), "rollback incompleto") {
		t.Fatalf("Install = %v", err)
	}
	_, exe, plist := paths(home)
	if got := readFile(t, exe); got != "old-binary" {
		t.Fatalf("executable after rollback = %q", got)
	}
	if got := readFile(t, plist); got != "old-plist" {
		t.Fatalf("plist after rollback = %q", got)
	}
	equalCalls(t, f.calls, []string{
		"sign",
		"bootout gui/501/" + LaunchAgentLabel,
		"bootstrap gui/501 " + LaunchAgentLabel + ".plist",
		"bootout gui/501/" + LaunchAgentLabel,
		"bootstrap gui/501 " + LaunchAgentLabel + ".plist", // previous agent reloaded
	})
	assertNoStaging(t, home)
}

func TestInstallFreshRollsBackWhenBootstrapFails(t *testing.T) {
	deps, f, home := testDarwinDeps(t)
	f.bootstrapErr = []error{errors.New("bootstrap failed")}
	if err := Install(deps); err == nil || !strings.Contains(err.Error(), "registrar o LaunchAgent") {
		t.Fatalf("Install = %v", err)
	}
	bundle, _, plist := paths(home)
	for _, path := range []string{bundle, plist} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s remains after rollback: %v", path, err)
		}
	}
	if last := f.calls[len(f.calls)-1]; last != "bootout gui/501/"+LaunchAgentLabel {
		t.Fatalf("new agent not unloaded or old agent reloaded: %v", f.calls)
	}
	assertNoStaging(t, home)
}

func TestInstallKeepsExistingBundleWhenSwapFails(t *testing.T) {
	deps, f, home := testDarwinDeps(t)
	writeExisting(t, home)
	deps.Ops.Swap = func(string, string) error { return errors.New("swap failed") }
	if err := Install(deps); err == nil || !strings.Contains(err.Error(), "instalar o app") {
		t.Fatalf("Install = %v", err)
	}
	_, exe, plist := paths(home)
	if readFile(t, exe) != "old-binary" || readFile(t, plist) != "old-plist" {
		t.Fatal("existing installation changed")
	}
	if len(f.calls) != 1 { // sign only
		t.Fatalf("calls = %v", f.calls)
	}
	assertNoStaging(t, home)
}

func TestSwapIntoPlaceRequiresSameParent(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := swapIntoPlace(filepath.Join(dir, "a", "x"), filepath.Join(dir, "b", "x"), Operations{}.withDefaults()); err == nil {
		t.Fatal("cross-directory swap accepted")
	}
}

type commandLog struct {
	calls   []string
	respond func(name string, args []string) ([]byte, error)
}

func (c *commandLog) run(name string, args ...string) ([]byte, error) {
	c.calls = append(c.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	return c.respond(name, args)
}

const identityHash = "17E2BB0B126F3EB10C124DCF08CB4218A8999A1E"

func signingOps(t *testing.T, c *commandLog) Operations {
	var logs []string
	tmp := t.TempDir()
	return Operations{
		RunCommand: c.run,
		MkdirTemp:  func(_, pattern string) (string, error) { return os.MkdirTemp(tmp, pattern) },
		Logf:       func(format string, _ ...any) { logs = append(logs, format) },
	}.withDefaults()
}

func TestSignBundleReusesExistingIdentity(t *testing.T) {
	c := &commandLog{respond: func(name string, args []string) ([]byte, error) {
		if args[0] == "find-identity" {
			return []byte(`  1) ` + identityHash + ` "Barroso Wispr Local" (CSSMERR_TP_NOT_TRUSTED)`), nil
		}
		return nil, nil
	}}
	ops := signingOps(t, c)
	if err := ops.SignBundle("/x/B.app", "/k/login.keychain-db"); err != nil {
		t.Fatal(err)
	}
	equalCalls(t, c.calls, []string{
		"security find-identity -p codesigning /k/login.keychain-db",
		"codesign --force --sign " + identityHash + " --keychain /k/login.keychain-db --identifier " + BundleIdentifier + " --timestamp=none /x/B.app",
	})
}

func TestSignBundleCreatesIdentityWhenMissing(t *testing.T) {
	finds := 0
	c := &commandLog{respond: func(name string, args []string) ([]byte, error) {
		if args[0] == "find-identity" {
			finds++
			if finds == 1 {
				return []byte("0 identities found"), nil
			}
			return []byte(`  1) ` + strings.ToLower(identityHash) + ` "Barroso Wispr Local"`), nil
		}
		return nil, nil
	}}
	ops := signingOps(t, c)
	if err := ops.SignBundle("/x/B.app", "/k/login.keychain-db"); err != nil {
		t.Fatal(err)
	}
	var tools []string
	for _, call := range c.calls {
		fields := strings.Fields(call)
		tools = append(tools, fields[0]+" "+fields[1])
	}
	equalCalls(t, tools, []string{
		"security find-identity", "openssl req", "openssl pkcs12", "security import", "security find-identity", "codesign --force",
	})
	if !strings.Contains(c.calls[3], "-T /usr/bin/codesign") || !strings.Contains(c.calls[3], "-k /k/login.keychain-db") {
		t.Fatalf("import call = %q", c.calls[3])
	}
	if !strings.Contains(c.calls[5], "--sign "+identityHash) {
		t.Fatalf("codesign call = %q", c.calls[5])
	}
}

func TestSignBundleFallsBackToAdHoc(t *testing.T) {
	for name, failIdentity := range map[string]bool{"identity unavailable": true, "local codesign rejected": false} {
		t.Run(name, func(t *testing.T) {
			c := &commandLog{respond: func(_ string, args []string) ([]byte, error) {
				switch {
				case args[0] == "find-identity" && failIdentity:
					return []byte("keychain locked"), errors.New("exit 1")
				case args[0] == "find-identity":
					return []byte(identityHash + ` "Barroso Wispr Local"`), nil
				case args[0] == "--force" && args[2] != "-":
					return []byte("untrusted"), errors.New("exit 1")
				}
				return nil, nil
			}}
			ops := signingOps(t, c)
			if err := ops.SignBundle("/x/B.app", "/k/login.keychain-db"); err != nil {
				t.Fatal(err)
			}
			if last := c.calls[len(c.calls)-1]; last != "codesign --force --sign - --identifier "+BundleIdentifier+" --timestamp=none /x/B.app" {
				t.Fatalf("last call = %q", last)
			}
		})
	}
}

func TestDefaultLaunchOperationsUseUserDomain(t *testing.T) {
	c := &commandLog{respond: func(_ string, args []string) ([]byte, error) {
		if args[0] == "bootout" {
			return []byte("Boot-out failed: 3: No such process"), errors.New("exit status 3")
		}
		return nil, nil
	}}
	ops := Operations{RunCommand: c.run}.withDefaults()
	if err := ops.Bootout("501", LaunchAgentLabel); err != nil {
		t.Fatalf("not-loaded bootout should succeed: %v", err)
	}
	if err := ops.Bootstrap("501", "/h/Library/LaunchAgents/a.plist"); err != nil {
		t.Fatal(err)
	}
	equalCalls(t, c.calls, []string{
		"launchctl bootout gui/501/" + LaunchAgentLabel,
		"launchctl bootstrap gui/501 /h/Library/LaunchAgents/a.plist",
	})

	failing := Operations{RunCommand: func(string, ...string) ([]byte, error) {
		return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
	}}.withDefaults()
	if err := failing.Bootout("501", LaunchAgentLabel); err == nil {
		t.Fatal("unexpected bootout failure ignored")
	}
}

func TestLaunchAgentPlistIsWellFormedAndValidated(t *testing.T) {
	if _, err := LaunchAgentPlist("/a", ""); err == nil {
		t.Fatal("empty key accepted")
	}
	data, err := LaunchAgentPlist("/Users/a&b/Applications/Barroso Wispr.app/Contents/MacOS/barroso-wispr", `k<"&'>`)
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(strings.NewReader(data))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity
	for {
		if _, err := dec.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("plist is not well-formed: %v\n%s", err, data)
		}
	}
	if !strings.Contains(data, "a&amp;b") {
		t.Fatalf("path not escaped: %s", data)
	}
}
