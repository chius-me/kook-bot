package secret

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := New([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, err := box.Seal(PurposeBotToken, []byte("bot-token-value"))
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != 12 {
		t.Fatalf("nonce len %d", len(nonce))
	}
	got, err := box.Open(PurposeBotToken, nonce, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("bot-token-value")) {
		t.Fatalf("plain = %q", got)
	}
	nonce2, ct2, err := box.Seal(PurposeBotToken, []byte("bot-token-value"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(nonce, nonce2) || bytes.Equal(ct, ct2) {
		t.Fatal("nonce reused")
	}
}

func TestOpenRejectsWrongPurpose(t *testing.T) {
	box, err := New([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, err := box.Seal(PurposeBotToken, []byte("bot-token-value"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = box.Open(PurposeTTSAPIKey, nonce, ct)
	if !errorsIs(err, ErrSecretMismatch) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "bot-token-value") {
		t.Fatal("error contains plaintext")
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	a, err := New([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := New([]byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, err := a.Seal(PurposeVoicePassword, []byte("room"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(PurposeVoicePassword, nonce, ct); !errorsIs(err, ErrSecretMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestFingerprint(t *testing.T) {
	a := Fingerprint([]byte("one-one-one-one-one-one-one-one"))
	b := Fingerprint([]byte("two-two-two-two-two-two-two-two"))
	if len(a) != 8 || a == b {
		t.Fatalf("a=%s b=%s", a, b)
	}
	if Fingerprint([]byte("one-one-one-one-one-one-one-one")) != a {
		t.Fatal("fingerprint not stable")
	}
}

func TestPasswordMatchIgnoresLengthLeak(t *testing.T) {
	if !PasswordMatch("same", "same") {
		t.Fatal("expected match")
	}
	if PasswordMatch("short", "much-longer-password") {
		t.Fatal("expected mismatch")
	}
}

func errorsIs(err, target error) bool {
	return err != nil && target != nil && err.Error() == target.Error()
}
