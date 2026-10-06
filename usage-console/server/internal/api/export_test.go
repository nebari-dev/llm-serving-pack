package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/receiptsig"
)

func unzip(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	return out
}

// §5.1, §9.2: an export is JSON Lines (the export's own description first,
// then each settled receipt, oldest first, as GET /receipts/{id} has it), a
// detached Ed25519 signature over those exact bytes, the public key and how
// to check it.
func TestExportBundleVerifiesAndDetectsTampering(t *testing.T) {
	sg, err := receiptsig.LoadOrCreate(filepath.Join(t.TempDir(), "k.pem"))
	if err != nil {
		t.Fatal(err)
	}
	cost := 0.0012
	rs := []model.Receipt{
		{ID: "r3", TS: 3_000, Verdict: "allowed", CostUSD: &cost},
		{ID: "r2", TS: 2_000, Verdict: "allowed", InFlight: true},
		{ID: "r1", TS: 1_000, Verdict: "blocked"},
	}
	h := exportHeader{Tenant: "demo", ExportedAt: 9_000, ExportedBy: "dev@localhost", Filter: exportFilter{Since: 500, Verdicts: []string{"allowed", "blocked"}}}
	b, ex, err := buildExport(h, rs, sg)
	if err != nil {
		t.Fatal(err)
	}
	files := unzip(t, b)
	for _, name := range []string{"receipts.jsonl", "receipts.jsonl.sig", "signing-key.pem", "README.txt"} {
		if len(files[name]) == 0 {
			t.Fatalf("bundle has no %s (has %v)", name, len(files))
		}
	}
	jsonl, sig := files["receipts.jsonl"], files["receipts.jsonl.sig"]
	if err := receiptsig.Verify(sg.PublicPEM(), jsonl, sig); err != nil {
		t.Fatalf("export doesn't verify: %v", err)
	}
	if !bytes.Equal(files["signing-key.pem"], sg.PublicPEM()) {
		t.Fatal("bundled key isn't the signing key's public half")
	}
	if !strings.Contains(string(files["README.txt"]), "openssl pkeyutl -verify") || !strings.Contains(string(files["README.txt"]), sg.KeyID()) {
		t.Fatalf("README doesn't say how to verify:\n%s", files["README.txt"])
	}

	lines := strings.Split(strings.TrimSuffix(string(jsonl), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines, want the header and 2 settled receipts:\n%s", len(lines), jsonl)
	}
	var head struct{ Export exportHeader }
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatal(err)
	}
	if head.Export.Count != 2 || head.Export.InFlightLeftOut != 1 || head.Export.KeyID != sg.KeyID() || head.Export.Algorithm != "Ed25519" || head.Export.ExportedBy != "dev@localhost" {
		t.Fatalf("header %+v", head.Export)
	}
	if ex.Count != 2 || ex.SHA256 == "" {
		t.Fatalf("export summary %+v", ex)
	}
	want, _ := json.Marshal(rs[2])
	if lines[1] != string(want) {
		t.Fatalf("line 2 = %s, want the oldest receipt as the API returns it: %s", lines[1], want)
	}
	if !strings.Contains(lines[2], `"id":"r3"`) {
		t.Fatalf("line 3 = %s", lines[2])
	}

	// One changed byte, or a dropped receipt, fails.
	if err := receiptsig.Verify(sg.PublicPEM(), bytes.Replace(jsonl, []byte("0.0012"), []byte("0.0011"), 1), sig); err == nil {
		t.Fatal("a changed cost verified")
	}
	if err := receiptsig.Verify(sg.PublicPEM(), []byte(lines[0]+"\n"+lines[1]+"\n"), sig); err == nil {
		t.Fatal("an export with a receipt removed verified")
	}
}

// Each export writes an audit row: who (the row's actor), the filter, the
// count and what was signed, so the bytes can be matched to the row later.
func TestExportAuditRowSaysWhatWasExported(t *testing.T) {
	ex := exportSummary{Count: 12, SHA256: "abc", KeyID: "k1"}
	action, target, after := exportAudit(exportFilter{Since: 1, Teams: []string{"support"}}, ex)
	if action != "Exported receipts" || target != "12 receipts" {
		t.Fatalf("%q %q", action, target)
	}
	b, _ := json.Marshal(after)
	for _, want := range []string{`"count":12`, `"sha256":"abc"`, `"keyId":"k1"`, `"team":["support"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("audit after %s lacks %s", b, want)
		}
	}
	action, target, _ = exportAudit(exportFilter{ID: "r1"}, exportSummary{Count: 1})
	if action != "Exported receipt" || target != "r1" {
		t.Fatalf("single: %q %q", action, target)
	}
}

// An export past the cap is refused, saying how to narrow it, rather than
// cut short: a signed file must hold everything its filter says it does.
func TestExportRefusesMoreThanTheCap(t *testing.T) {
	sg, _ := receiptsig.LoadOrCreate(filepath.Join(t.TempDir(), "k.pem"))
	rs := make([]model.Receipt, exportCap+1)
	_, _, err := buildExport(exportHeader{}, rs, sg)
	if err == nil || !strings.Contains(err.Error(), "narrow") {
		t.Fatalf("err = %v", err)
	}
}

// The README's command works: openssl verifies the bundle as written, and
// refuses it with a byte changed. Skipped without OpenSSL 3 on the PATH.
func TestExportVerifiesWithOpenSSL(t *testing.T) {
	if out, err := exec.Command("openssl", "version").Output(); err != nil || !strings.HasPrefix(string(out), "OpenSSL 3") {
		t.Skip("needs OpenSSL 3")
	}
	sg, _ := receiptsig.LoadOrCreate(filepath.Join(t.TempDir(), "k.pem"))
	b, _, err := buildExport(exportHeader{Tenant: "demo"}, []model.Receipt{{ID: "r1", TS: 1}}, sg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, body := range unzip(t, b) {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	verify := func() (string, error) {
		cmd := exec.Command("openssl", "pkeyutl", "-verify", "-pubin", "-inkey", "signing-key.pem", "-rawin", "-in", "receipts.jsonl", "-sigfile", "receipts.jsonl.sig")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := verify(); err != nil || !strings.Contains(out, "Signature Verified Successfully") {
		t.Fatalf("openssl: %v\n%s", err, out)
	}
	// The key id is the SHA-256 of the DER public key, as the README says.
	cmd := exec.Command("openssl", "pkey", "-pubin", "-in", "signing-key.pem", "-outform", "DER")
	cmd.Dir = dir
	der, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(der); hex.EncodeToString(sum[:8]) != sg.KeyID() {
		t.Fatalf("key id %s isn't the DER key's SHA-256 prefix", sg.KeyID())
	}
	jsonl, _ := os.ReadFile(filepath.Join(dir, "receipts.jsonl"))
	os.WriteFile(filepath.Join(dir, "receipts.jsonl"), bytes.Replace(jsonl, []byte(`"r1"`), []byte(`"r2"`), 1), 0o600)
	if out, err := verify(); err == nil {
		t.Fatalf("openssl verified a changed file:\n%s", out)
	}
}
