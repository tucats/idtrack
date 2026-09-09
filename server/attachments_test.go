package server

import (
	"bytes"
	"encoding/json"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tucats/idtrack/db"
)

// multipartUploadReq builds an httptest.Request carrying a single-file
// multipart/form-data body under the attachmentFormField field name, plus
// an optional Bearer token — the upload-route counterpart to jsonReq in
// testhelpers_test.go.
func multipartUploadReq(t *testing.T, path, filename string, data []byte, token string) *http.Request {
	t.Helper()

	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)

	part, err := mw.CreateFormFile(attachmentFormField, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}

	if _, err := part.Write(data); err != nil {
		t.Fatalf("write form file: %v", err)
	}

	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, path, &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())

	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}

	return r
}

func TestHandleCreateIssueAttachment_PDF(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, testUser, false)
	db.CreateIssue(s.database, "T", "", testUser, "", "Medium", "p", "c", "")

	pdfBytes := []byte("%PDF-1.7\nfake pdf content\n%%EOF")

	r := multipartUploadReq(t, "/api/issues/1/attachments", "report.pdf", pdfBytes, token)
	r.SetPathValue("id", "1")
	w := do(s, s.handleCreateIssueAttachment, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	attachment, _ := resp["attachment"].(map[string]interface{})
	if attachment == nil {
		t.Fatal("expected 'attachment' key in response")
	}

	if attachment["type"] != "pdf" {
		t.Errorf("type: got %v, want pdf", attachment["type"])
	}

	if attachment["filename"] != "report.pdf" {
		t.Errorf("filename: got %v, want report.pdf", attachment["filename"])
	}
}

func TestHandleCreateIssueAttachment_UnsupportedFormat(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, testUser, false)
	db.CreateIssue(s.database, "T", "", testUser, "", "Medium", "p", "c", "")

	garbage := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x00}

	r := multipartUploadReq(t, "/api/issues/1/attachments", "mystery.bin", garbage, token)
	r.SetPathValue("id", "1")
	w := do(s, s.handleCreateIssueAttachment, r)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status: got %d, want %d: %s", w.Code, http.StatusUnsupportedMediaType, w.Body.String())
	}
}

func TestHandleCreateIssueAttachment_IssueNotFound(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, testUser, false)

	r := multipartUploadReq(t, "/api/issues/999/attachments", "notes.txt", []byte("hello world\n"), token)
	r.SetPathValue("id", "999")
	w := do(s, s.handleCreateIssueAttachment, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandleGetAttachmentPage(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, testUser, false)
	db.CreateIssue(s.database, "T", "", testUser, "", "Medium", "p", "c", "")

	pdfBytes := readTestdataPDF(t, "two-pages.pdf")

	uploadReq := multipartUploadReq(t, "/api/issues/1/attachments", "two-pages.pdf", pdfBytes, token)
	uploadReq.SetPathValue("id", "1")
	uploadW := do(s, s.handleCreateIssueAttachment, uploadReq)

	if uploadW.Code != http.StatusCreated {
		t.Fatalf("upload status: got %d: %s", uploadW.Code, uploadW.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(uploadW.Body).Decode(&resp)
	attachment := resp["attachment"].(map[string]interface{})

	if got := attachment["pages"]; got != float64(2) {
		t.Fatalf("attachment pages = %v, want 2", got)
	}

	id := attachment["id"].(string)

	for _, page := range []string{"1", "2"} {
		t.Run("page "+page, func(t *testing.T) {
			r := jsonReq(t, http.MethodGet, "/api/attachments/"+id+"/page/"+page, "", token)
			r.SetPathValue("aid", id)
			r.SetPathValue("page", page)
			w := do(s, s.handleGetAttachmentPage, r)

			if w.Code != http.StatusOK {
				t.Fatalf("status: got %d: %s", w.Code, w.Body.String())
			}

			if ct := w.Header().Get("Content-Type"); ct != "image/png" {
				t.Errorf("Content-Type: got %q, want image/png", ct)
			}

			if _, err := png.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
				t.Errorf("page image should be a valid PNG: %v", err)
			}
		})
	}

	t.Run("out of range", func(t *testing.T) {
		r := jsonReq(t, http.MethodGet, "/api/attachments/"+id+"/page/3", "", token)
		r.SetPathValue("aid", id)
		r.SetPathValue("page", "3")
		w := do(s, s.handleGetAttachmentPage, r)

		if w.Code != http.StatusNotFound {
			t.Errorf("status: got %d, want %d", w.Code, http.StatusNotFound)
		}
	})

	t.Run("invalid page number", func(t *testing.T) {
		r := jsonReq(t, http.MethodGet, "/api/attachments/"+id+"/page/0", "", token)
		r.SetPathValue("aid", id)
		r.SetPathValue("page", "0")
		w := do(s, s.handleGetAttachmentPage, r)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
		}
	})
}

// TestHandleGetAttachmentPage_LegacyZeroPagesIsBackfilled simulates a PDF
// attachment created before this feature existed (pages stored as 0 — see
// db.SetAttachmentPages's doc comment) and confirms the page endpoint
// computes and caches the count lazily on first request rather than failing.
func TestHandleGetAttachmentPage_LegacyZeroPagesIsBackfilled(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, testUser, false)
	db.CreateIssue(s.database, "T", "", testUser, "", "Medium", "p", "c", "")

	pdfBytes := readTestdataPDF(t, "two-pages.pdf")

	a, err := db.CreateAttachment(s.database, 1, 0, testUser, "legacy.pdf", db.AttachmentPDF, pdfBytes, []byte("thumb"), 0, 0, 0)
	if err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}

	r := jsonReq(t, http.MethodGet, "/api/attachments/"+a.ID+"/page/2", "", token)
	r.SetPathValue("aid", a.ID)
	r.SetPathValue("page", "2")
	w := do(s, s.handleGetAttachmentPage, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d: %s", w.Code, w.Body.String())
	}

	got, err := db.GetAttachment(s.database, a.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}

	if got.Pages != 2 {
		t.Errorf("expected Pages to be backfilled to 2, got %d", got.Pages)
	}
}

func TestHandleGetAttachmentImage_ContentTypePerKind(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, testUser, false)
	db.CreateIssue(s.database, "T", "", testUser, "", "Medium", "p", "c", "")

	cases := []struct {
		name        string
		filename    string
		data        []byte
		wantType    string
		wantContent string
	}{
		{"text", "notes.txt", []byte("hello, world\nsecond line\n"), "text", "text/plain; charset=utf-8"},
		{"pdf", "doc.pdf", []byte("%PDF-1.4\nbody\n%%EOF"), "pdf", "application/pdf"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uploadReq := multipartUploadReq(t, "/api/issues/1/attachments", tc.filename, tc.data, token)
			uploadReq.SetPathValue("id", "1")
			uploadW := do(s, s.handleCreateIssueAttachment, uploadReq)

			if uploadW.Code != http.StatusCreated {
				t.Fatalf("upload status: got %d: %s", uploadW.Code, uploadW.Body.String())
			}

			var resp map[string]interface{}
			json.NewDecoder(uploadW.Body).Decode(&resp)
			attachment := resp["attachment"].(map[string]interface{})

			if attachment["type"] != tc.wantType {
				t.Fatalf("type: got %v, want %v", attachment["type"], tc.wantType)
			}

			id := attachment["id"].(string)

			getReq := jsonReq(t, http.MethodGet, "/api/attachments/"+id, "", token)
			getReq.SetPathValue("aid", id)
			getW := do(s, s.handleGetAttachmentImage, getReq)

			if getW.Code != http.StatusOK {
				t.Fatalf("get status: got %d", getW.Code)
			}

			if ct := getW.Header().Get("Content-Type"); ct != tc.wantContent {
				t.Errorf("Content-Type: got %q, want %q", ct, tc.wantContent)
			}

			if !bytes.Equal(getW.Body.Bytes(), tc.data) {
				t.Error("full blob should be the raw original bytes, unchanged")
			}

			// Thumbnail is always a PNG, regardless of the parent kind.
			thumbReq := jsonReq(t, http.MethodGet, "/api/attachments/"+id+"/thumbnail", "", token)
			thumbReq.SetPathValue("aid", id)
			thumbW := do(s, s.handleGetAttachmentThumbnail, thumbReq)

			if thumbW.Code != http.StatusOK {
				t.Fatalf("thumbnail status: got %d", thumbW.Code)
			}

			if ct := thumbW.Header().Get("Content-Type"); ct != "image/png" {
				t.Errorf("thumbnail Content-Type: got %q, want image/png", ct)
			}
		})
	}
}
