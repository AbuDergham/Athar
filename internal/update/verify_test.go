package update

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"aead.dev/minisign"
)

func newKey(t *testing.T) (minisign.PublicKey, minisign.PrivateKey) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestParsePublicKey(t *testing.T) {
	pub, _ := newKey(t)
	full, err := pub.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		in      string
		wantErr error // nil: must parse to pub
		anyErr  bool
	}{
		{name: "bare base64 line (tools/keygen output)", in: pub.String()},
		{name: "whole .pub file", in: string(full)},
		{name: "whole .pub file with CRLF and padding", in: "\r\n" + strings.ReplaceAll(string(full), "\n", "\r\n") + "\r\n"},
		{name: "empty", in: "", wantErr: ErrNoPublicKey},
		{name: "blank", in: " \n\t", wantErr: ErrNoPublicKey},
		{name: "garbage", in: "not a key", anyErr: true},
		{name: "truncated", in: pub.String()[:20], anyErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePublicKey(tt.in)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.anyErr:
				if err == nil || errors.Is(err, ErrNoPublicKey) {
					t.Fatalf("err = %v, want a parse error", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if !got.Equal(pub) {
					t.Fatalf("parsed key %s, want %s", got, pub)
				}
			}
		})
	}
}

func TestVerifySignature(t *testing.T) {
	pub, priv := newKey(t)
	_, otherPriv := newKey(t)
	data := []byte("0123abcd  athar_1.1.0_windows_amd64.exe\n")
	good := minisign.Sign(priv, data)

	var prehashed []byte // minisign CLI default (HashEdDSA)
	{
		r := minisign.NewReader(bytes.NewReader(data))
		if _, err := r.Read(make([]byte, len(data)+1)); err != nil {
			t.Fatal(err)
		}
		prehashed = r.Sign(priv)
	}
	tampered := append([]byte(nil), data...)
	tampered[0] = '9'

	tests := []struct {
		name string
		data []byte
		sig  []byte
		ok   bool
	}{
		{"good", data, good, true},
		{"good prehashed", data, prehashed, true},
		{"tampered data", tampered, good, false},
		{"other key", data, minisign.Sign(otherPriv, data), false},
		{"garbage signature", data, []byte("untrusted comment: x\nAAAA\n"), false},
		{"empty signature", data, nil, false},
	}
	for _, tt := range tests {
		err := verifySignature(pub, tt.data, tt.sig)
		if tt.ok && err != nil {
			t.Errorf("%s: unexpected error %v", tt.name, err)
		}
		if !tt.ok && !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: err = %v, want ErrBadSignature", tt.name, err)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	exeSum, tarSum := sum("exe"), sum("tar")
	goreleaser := exeSum + "  athar_1.1.0_windows_amd64.exe\n" +
		tarSum + "  athar_1.1.0_linux_amd64.tar.gz\n" +
		sum("arm") + "  athar_1.1.0_windows_arm64.exe\n"

	tests := []struct {
		name    string
		sums    string
		file    string
		want    string
		wantErr bool
	}{
		{name: "goreleaser format", sums: goreleaser, file: "athar_1.1.0_windows_amd64.exe", want: exeSum},
		{name: "other arch", sums: goreleaser, file: "athar_1.1.0_windows_arm64.exe", want: sum("arm")},
		{name: "CRLF", sums: strings.ReplaceAll(goreleaser, "\n", "\r\n"), file: "athar_1.1.0_linux_amd64.tar.gz", want: tarSum},
		{name: "binary marker", sums: exeSum + " *athar_1.1.0_windows_amd64.exe\n", file: "athar_1.1.0_windows_amd64.exe", want: exeSum},
		{name: "upper-case hex", sums: strings.ToUpper(exeSum) + "  athar_1.1.0_windows_amd64.exe", file: "athar_1.1.0_windows_amd64.exe", want: exeSum},
		{name: "missing", sums: goreleaser, file: "athar_1.2.0_windows_amd64.exe", wantErr: true},
		{name: "suffix is not a match", sums: exeSum + "  xathar_1.1.0_windows_amd64.exe\n", file: "athar_1.1.0_windows_amd64.exe", wantErr: true},
		{name: "malformed hex", sums: "zz" + exeSum[2:] + "  athar_1.1.0_windows_amd64.exe\n", file: "athar_1.1.0_windows_amd64.exe", wantErr: true},
		{name: "short hash", sums: exeSum[:32] + "  athar_1.1.0_windows_amd64.exe\n", file: "athar_1.1.0_windows_amd64.exe", wantErr: true},
		{name: "empty", sums: "", file: "athar_1.1.0_windows_amd64.exe", wantErr: true},
	}
	for _, tt := range tests {
		got, err := checksumFor([]byte(tt.sums), tt.file)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tt.name, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && hex.EncodeToString(got) != tt.want {
			t.Errorf("%s: got %x, want %s", tt.name, got, tt.want)
		}
	}
}
