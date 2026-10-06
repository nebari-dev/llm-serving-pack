package api

import (
	"archive/zip"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/receiptsig"
	"github.com/jbouder/stargate/server/internal/store"
)

// Signed receipt export (spec §5.1, §7.5.4, §9.2). An export is a zip of
//
//	receipts.jsonl      line 1 {"export": exportHeader}, then one settled
//	                    receipt per line, oldest first, as GET /receipts/{id}
//	                    returns it (never content, which that leaves out)
//	receipts.jsonl.sig  a detached Ed25519 signature, 64 raw bytes, over
//	                    receipts.jsonl exactly
//	signing-key.pem     the public key (also GET /receipts/signing-key)
//	README.txt          how to verify it with openssl
//
// and writes an audit row (who, the filter, the count, the SHA-256 of what
// was signed) before the response goes out.

// exportCap is the most receipts one export holds. The whole file is signed
// at once, so it's built in memory: about 2 KB a receipt, 20 MB at the cap.
const exportCap = 10_000

// exportFilter is what an export selected, in GET /receipts' query names.
type exportFilter struct {
	ID        string   `json:"id,omitempty"`
	Since     int64    `json:"since,omitempty"`
	Before    int64    `json:"before,omitempty"`
	Keys      []string `json:"key,omitempty"`
	Teams     []string `json:"team,omitempty"`
	Projects  []string `json:"project,omitempty"`
	Models    []string `json:"model,omitempty"`
	Verdicts  []string `json:"verdict,omitempty"`
	Providers []string `json:"provider,omitempty"`
	Backends  []string `json:"backend,omitempty"`
	Reasons   []string `json:"reason,omitempty"`
	Sessions  []string `json:"session,omitempty"`
}

func filterOf(q store.ReceiptQuery) exportFilter {
	return exportFilter{Since: q.Since, Before: q.Before, Keys: q.Keys, Teams: q.Teams, Projects: q.Projects, Models: q.Models,
		Verdicts: q.Verdicts, Providers: q.Providers, Backends: q.Backends, Reasons: q.Reasons, Sessions: q.Sessions}
}

// exportHeader is the first line of receipts.jsonl, so what the export
// claims to be is signed with it.
type exportHeader struct {
	Tenant          string       `json:"tenant"`
	ExportedAt      int64        `json:"exportedAt"` // epoch ms
	ExportedBy      string       `json:"exportedBy"`
	Filter          exportFilter `json:"filter"`
	Count           int          `json:"count"`
	InFlightLeftOut int          `json:"inFlightLeftOut"` // still streaming at export time, so not final
	KeyID           string       `json:"keyId"`
	Algorithm       string       `json:"algorithm"`
}

// exportSummary is what the audit row records about an export.
type exportSummary struct {
	Count  int
	SHA256 string // of receipts.jsonl
	KeyID  string
}

// buildExport makes the bundle. A filter matching more than exportCap is
// refused rather than cut short: the file must hold everything it says.
func buildExport(h exportHeader, rs []model.Receipt, sg *receiptsig.Signer) ([]byte, exportSummary, error) {
	if len(rs) > exportCap {
		return nil, exportSummary{}, badRequest(fmt.Sprintf("More than %d receipts match: narrow the time range or add a filter, then export again.", exportCap))
	}
	settled := make([]model.Receipt, 0, len(rs))
	for _, r := range rs {
		if r.InFlight {
			h.InFlightLeftOut++
			continue
		}
		settled = append(settled, r)
	}
	slices.SortStableFunc(settled, func(a, b model.Receipt) int { return cmp.Compare(a.TS, b.TS) })
	h.Count, h.KeyID, h.Algorithm = len(settled), sg.KeyID(), "Ed25519"

	var jsonl bytes.Buffer
	line := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		jsonl.Write(b)
		jsonl.WriteByte('\n')
		return nil
	}
	if err := line(map[string]any{"export": h}); err != nil {
		return nil, exportSummary{}, err
	}
	for _, r := range settled {
		if err := line(r); err != nil {
			return nil, exportSummary{}, err
		}
	}
	sum := sha256.Sum256(jsonl.Bytes())
	ex := exportSummary{Count: h.Count, SHA256: hex.EncodeToString(sum[:]), KeyID: sg.KeyID()}

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	at := time.UnixMilli(h.ExportedAt)
	for _, f := range []struct {
		name string
		b    []byte
	}{
		{"receipts.jsonl", jsonl.Bytes()},
		{"receipts.jsonl.sig", sg.Sign(jsonl.Bytes())},
		{"signing-key.pem", sg.PublicPEM()},
		{"README.txt", []byte(exportReadme(h, ex))},
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: at})
		if err != nil {
			return nil, exportSummary{}, err
		}
		if _, err := w.Write(f.b); err != nil {
			return nil, exportSummary{}, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, exportSummary{}, err
	}
	return out.Bytes(), ex, nil
}

func exportReadme(h exportHeader, ex exportSummary) string {
	filter, _ := json.Marshal(h.Filter)
	var b strings.Builder
	fmt.Fprintf(&b, "Stargate receipt export\n\n")
	fmt.Fprintf(&b, "Exported %s by %s from tenant %s.\n", time.UnixMilli(h.ExportedAt).UTC().Format(time.RFC3339), h.ExportedBy, h.Tenant)
	fmt.Fprintf(&b, "Filter: %s\n", filter)
	fmt.Fprintf(&b, "Receipts: %d", h.Count)
	if h.InFlightLeftOut > 0 {
		fmt.Fprintf(&b, " (%d still streaming at export time were left out)", h.InFlightLeftOut)
	}
	fmt.Fprintf(&b, "\nSigned with Ed25519 key %s. SHA-256 of receipts.jsonl: %s\n\n", ex.KeyID, ex.SHA256)
	b.WriteString(`Files
  receipts.jsonl      Line 1 describes this export. Every other line is one receipt.
                      Receipts hold hashes of the request and response, never their content.
  receipts.jsonl.sig  Ed25519 signature (64 raw bytes) over receipts.jsonl exactly.
  signing-key.pem     The public key that made the signature.

Verify (OpenSSL 3)
  First check that signing-key.pem is the key your Stargate publishes at
  GET /api/v1/` + h.Tenant + `/receipts/signing-key, not only the copy in this file. Then:

    openssl pkeyutl -verify -pubin -inkey signing-key.pem -rawin -in receipts.jsonl -sigfile receipts.jsonl.sig

  It prints "Signature Verified Successfully". If any byte of receipts.jsonl
  changed, it prints "Signature Verification Failure".
`)
	return b.String()
}

// exportAudit is the audit row's action, target and after for an export.
func exportAudit(f exportFilter, ex exportSummary) (action, target string, after any) {
	after = map[string]any{"filter": f, "count": ex.Count, "sha256": ex.SHA256, "keyId": ex.KeyID}
	if f.ID != "" {
		return "Exported receipt", f.ID, after
	}
	if ex.Count == 1 {
		return "Exported receipts", "1 receipt", after
	}
	return "Exported receipts", fmt.Sprintf("%d receipts", ex.Count), after
}

// SigningKey is GET /receipts/signing-key: the public key exports verify
// against. The private key never leaves the control plane.
type SigningKey struct {
	KeyID        string `json:"keyId"`
	Algorithm    string `json:"algorithm"`
	PublicKeyPEM string `json:"publicKeyPem"`
}

func (s *Server) signingKey(_ http.ResponseWriter, _ *http.Request, _ string) (any, error) {
	if s.Signer == nil {
		return nil, unavailable("This control plane has no receipt signing key.")
	}
	return SigningKey{KeyID: s.Signer.KeyID(), Algorithm: "Ed25519", PublicKeyPEM: string(s.Signer.PublicPEM())}, nil
}

// exportReceipts is POST /receipts/export with GET /receipts' filters (limit
// is ignored): every settled receipt matching, up to exportCap.
func (s *Server) exportReceipts(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	if s.Signer == nil {
		return nil, unavailable("This control plane has no receipt signing key.")
	}
	q := receiptQuery(r)
	q.Limit = exportCap + 1
	rs, err := s.Store.ListReceipts(r.Context(), t, q)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return nil, s.sendExport(w, r, t, filterOf(q), rs, now, fmt.Sprintf("receipts-%s-%s.zip", t, now.UTC().Format("20060102T150405Z")))
}

// exportReceipt is POST /receipts/{id}/export: one receipt, from the drawer.
func (s *Server) exportReceipt(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	if s.Signer == nil {
		return nil, unavailable("This control plane has no receipt signing key.")
	}
	rc, err := s.Store.Receipt(r.Context(), t, r.PathValue("id"), 0)
	if err != nil {
		return nil, err
	}
	if rc.InFlight {
		return nil, conflict("This request is still streaming. Export it once it settles.")
	}
	return nil, s.sendExport(w, r, t, exportFilter{ID: rc.ID}, []model.Receipt{rc}, time.Now(), "receipt-"+rc.ID+".zip")
}

func (s *Server) sendExport(w http.ResponseWriter, r *http.Request, t string, f exportFilter, rs []model.Receipt, now time.Time, name string) error {
	h := exportHeader{Tenant: t, ExportedAt: now.UnixMilli(), ExportedBy: actor(r), Filter: f}
	b, ex, err := buildExport(h, rs, s.Signer)
	if err != nil {
		return err
	}
	// No export without its audit row: the row commits before any byte goes out.
	action, target, after := exportAudit(f, ex)
	if err := s.Store.Audited(r.Context(), t, actor(r), action, target, store.AccessKind, nil, after, func() error { return nil }); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("X-Stargate-Export-Count", fmt.Sprint(ex.Count))
	w.Header().Set("X-Stargate-Signing-Key", ex.KeyID)
	w.WriteHeader(http.StatusOK)
	w.Write(b) // too late for an error status; the client sees a short file
	return nil
}

// RevealedContent is POST /receipts/{id}/reveal.
type RevealedContent struct {
	Content    json.RawMessage `json:"content"`
	RevealedBy string          `json:"revealedBy"`
	RevealedAt int64           `json:"revealedAt"`
}

// revealContent returns a receipt's captured content (§7.5.4 section 5),
// after writing the audit row that says who looked (§9.2). A receipt
// without content is a 409 and writes no row: there was nothing to see.
func (s *Server) revealContent(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	id := r.PathValue("id")
	content, err := s.Store.ReceiptContent(r.Context(), t, id)
	if err != nil {
		return nil, err
	}
	if content == nil {
		return nil, conflict("Content wasn't captured for this request: only its hashes were stored.")
	}
	now := time.Now()
	if err := s.Store.Audited(r.Context(), t, actor(r), "Revealed content", id, store.AccessKind, nil, map[string]any{"receipt": id}, func() error { return nil }); err != nil {
		return nil, err
	}
	return RevealedContent{Content: content, RevealedBy: actor(r), RevealedAt: now.UnixMilli()}, nil
}
