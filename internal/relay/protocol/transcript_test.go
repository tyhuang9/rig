package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"
	"time"
)

func TestCanonicalACKDigestIsStableAndRejectsNonCanonicalState(t *testing.T) {
	state := []ACKState{{SubscriptionID: idDelivery, Generation: 7}, {SubscriptionID: idEvent, Generation: 9}}
	digest, err := CanonicalACKDigest(state)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "0a0de3a26fc767012a299ad7690f71a957f1a8542eb30d9e61f71caa26dc0a3b"
	if got := hex.EncodeToString(digest[:]); got != expected {
		t.Fatalf("digest = %s", got)
	}
	again, err := CanonicalACKDigest(append([]ACKState(nil), state...))
	if err != nil || digest != again {
		t.Fatalf("unstable digest: %x %x %v", digest, again, err)
	}
	for _, invalidState := range [][]ACKState{{state[1], state[0]}, {state[0], state[0]}, {{SubscriptionID: idDelivery, Generation: 0}}, nil} {
		if _, err := CanonicalACKDigest(invalidState); err == nil {
			t.Fatalf("invalid state accepted: %#v", invalidState)
		}
	}
	changed := append([]ACKState(nil), state...)
	changed[1].Generation++
	changedDigest, err := CanonicalACKDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest == digest {
		t.Fatal("generation change did not change digest")
	}
}

func TestLengthPrefixingIsUnambiguousAndDomainSeparated(t *testing.T) {
	if bytes.Equal(buildTranscript("domain", []byte("ab"), []byte("c")), buildTranscript("domain", []byte("a"), []byte("bc"))) {
		t.Fatal("field boundaries are ambiguous")
	}
	if bytes.Equal(buildTranscript("domain-a", []byte("value")), buildTranscript("domain-b", []byte("value"))) {
		t.Fatal("domains collide")
	}
}

func TestEnrollmentTranscriptBindsTargetAndReplayFields(t *testing.T) {
	base := EnrollmentProof{ControllerID: idController, KeyID: idKey, PublicKey: b64(PublicKeyBytes, 1), InstallationID: 11, RepositoryID: 22, RequestNonce: b64(NonceBytes, 2), IssuedAt: testTime, ExpiresAt: testTime.Add(time.Minute)}
	original := enrollment(t, base)
	mutations := []EnrollmentProof{base, base, base, base, base, base, base, base}
	mutations[0].ControllerID = idDelivery
	mutations[1].KeyID = idNewKey
	mutations[2].PublicKey = b64(PublicKeyBytes, 3)
	mutations[3].InstallationID++
	mutations[4].RepositoryID++
	mutations[5].RequestNonce = b64(NonceBytes, 4)
	mutations[6].IssuedAt = mutations[6].IssuedAt.Add(time.Second)
	mutations[7].ExpiresAt = mutations[7].ExpiresAt.Add(time.Second)
	for i, mutation := range mutations {
		if bytes.Equal(original, enrollment(t, mutation)) {
			t.Fatalf("mutation %d did not change transcript", i)
		}
	}
	invalid := base
	invalid.ExpiresAt = invalid.IssuedAt
	if _, err := EnrollmentTranscript(invalid); err == nil {
		t.Fatal("invalid enrollment window accepted")
	}
}

func TestWSSAuthenticationTranscriptBindsStoredSessionState(t *testing.T) {
	base := AuthenticationBinding{ControllerID: idController, KeyID: idKey, SessionID: idSession, ClientNonce: b64(NonceBytes, 1), ServerNonce: b64(NonceBytes, 2), ACKDigest: b64(DigestBytes, 3), ExpiresAt: testTime.Add(time.Minute)}
	original := authentication(t, base)
	mutations := []AuthenticationBinding{base, base, base, base, base, base, base}
	mutations[0].ControllerID = idDelivery
	mutations[1].KeyID = idNewKey
	mutations[2].SessionID = idRotation
	mutations[3].ClientNonce = b64(NonceBytes, 4)
	mutations[4].ServerNonce = b64(NonceBytes, 5)
	mutations[5].ACKDigest = b64(DigestBytes, 6)
	mutations[6].ExpiresAt = mutations[6].ExpiresAt.Add(time.Second)
	for i, mutation := range mutations {
		if bytes.Equal(original, authentication(t, mutation)) {
			t.Fatalf("mutation %d did not change transcript", i)
		}
	}
}

func TestRotationTranscriptBindsPossessionAndStoredChallenge(t *testing.T) {
	base := RotationProof{RotationID: idRotation, ControllerID: idController, OldKeyID: idKey, NewKeyID: idNewKey, NewPublicKey: b64(PublicKeyBytes, 1), SessionID: idSession, ServerNonce: b64(NonceBytes, 2), ExpiresAt: testTime.Add(time.Minute)}
	original := rotation(t, base)
	mutations := []RotationProof{base, base, base, base, base, base, base, base}
	mutations[0].RotationID = idDelivery
	mutations[1].ControllerID = idEvent
	mutations[2].OldKeyID = idDelivery
	mutations[3].NewKeyID = idEvent
	mutations[4].NewPublicKey = b64(PublicKeyBytes, 3)
	mutations[5].SessionID = idDelivery
	mutations[6].ServerNonce = b64(NonceBytes, 4)
	mutations[7].ExpiresAt = mutations[7].ExpiresAt.Add(time.Second)
	for i, mutation := range mutations {
		if bytes.Equal(original, rotation(t, mutation)) {
			t.Fatalf("mutation %d did not change transcript", i)
		}
	}
	if bytes.Equal(original, enrollment(t, EnrollmentProof{ControllerID: idController, KeyID: idKey, PublicKey: b64(PublicKeyBytes, 1), InstallationID: 1, RepositoryID: 2, RequestNonce: b64(NonceBytes, 2), IssuedAt: testTime, ExpiresAt: testTime.Add(time.Minute)})) {
		t.Fatal("cross-domain transcripts collide")
	}
}

func TestSignAndVerify(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{9}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	transcript := authentication(t, AuthenticationBinding{ControllerID: idController, KeyID: idKey, SessionID: idSession, ClientNonce: b64(NonceBytes, 1), ServerNonce: b64(NonceBytes, 2), ACKDigest: b64(DigestBytes, 3), ExpiresAt: testTime.Add(time.Minute)})
	signature, err := Sign(privateKey, transcript)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(publicKey, transcript, signature) {
		t.Fatal("valid signature rejected")
	}
	changed := append([]byte(nil), transcript...)
	changed[len(changed)-1] ^= 1
	if Verify(publicKey, changed, signature) {
		t.Fatal("changed transcript verified")
	}
	if Verify(publicKey, transcript, base64.RawURLEncoding.EncodeToString(make([]byte, 63))) {
		t.Fatal("short signature verified")
	}
}

func TestIssuerUsesInjectedEntropyAndClock(t *testing.T) {
	entropyBytes := bytes.Repeat([]byte{0x42}, 256)
	now := testTime
	issuer := Issuer{Entropy: bytes.NewReader(entropyBytes), Now: func() time.Time { return now }}
	hello, err := issuer.NewHello(idController, idKey, []ACKState{})
	if err != nil {
		t.Fatal(err)
	}
	if !hello.SentAt.Equal(now) || hello.MessageID == "" || hello.ClientNonce == "" {
		t.Fatalf("unexpected hello: %#v", hello)
	}
	issuer = Issuer{Entropy: bytes.NewReader(entropyBytes), Now: func() time.Time { return now }}
	hello2, err := issuer.NewHello(idController, idKey, []ACKState{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hello, hello2) {
		t.Fatal("injected issuer is not deterministic")
	}
	issuer = Issuer{Entropy: bytes.NewReader(entropyBytes), Now: func() time.Time { return now }}
	challenge, err := issuer.NewChallenge(time.Minute, []ACKState{})
	if err != nil {
		t.Fatal(err)
	}
	if !challenge.ExpiresAt.Equal(now.Add(time.Minute)) || challenge.SessionID == "" || challenge.ServerNonce == "" || challenge.ACKDigest == "" {
		t.Fatalf("unexpected challenge: %#v", challenge)
	}
	if _, err := NewNonce(bytes.NewReader(nil)); err == nil {
		t.Fatal("short entropy accepted")
	}
}

func TestTranscriptCanonicalSnapshot(t *testing.T) {
	transcript := authentication(t, AuthenticationBinding{ControllerID: idController, KeyID: idKey, SessionID: idSession, ClientNonce: b64(NonceBytes, 1), ServerNonce: b64(NonceBytes, 2), ACKDigest: b64(DigestBytes, 3), ExpiresAt: testTime.Add(time.Minute)})
	digest := sha256.Sum256(transcript)
	const expected = "b0286f220b023550a8838628720a61eddecc210e938046eef1287e55cbaeb17e"
	if got := hex.EncodeToString(digest[:]); got != expected {
		t.Fatalf("snapshot = %s", got)
	}
}

func TestTranscriptBytesMatchIndependentFixtures(t *testing.T) {
	version := make([]byte, 8)
	binary.BigEndian.PutUint64(version, 1)
	installation := make([]byte, 8)
	binary.BigEndian.PutUint64(installation, 11)
	repository := make([]byte, 8)
	binary.BigEndian.PutUint64(repository, 22)
	enrollmentValue := EnrollmentProof{ControllerID: idController, KeyID: idKey, PublicKey: b64(PublicKeyBytes, 1), InstallationID: 11, RepositoryID: 22, RequestNonce: b64(NonceBytes, 2), IssuedAt: testTime, ExpiresAt: testTime.Add(time.Minute)}
	enrollmentFixture := fixtureTranscript("rig.relay.v1/enrollment-possession", version, []byte(idController), []byte(idKey), []byte(enrollmentValue.PublicKey), installation, repository, []byte(enrollmentValue.RequestNonce), []byte("2026-08-24T15:04:05.123456789Z"), []byte("2026-08-24T15:05:05.123456789Z"))
	assertRawGolden(t, "enrollment", enrollmentFixture, "00000000000000227269672e72656c61792e76312f656e726f6c6c6d656e742d706f7373657373696f6e00000000000000080000000000000001000000000000002432323232323232322d323232322d343232322d383232322d323232323232323232323232000000000000002433333333333333332d333333332d343333332d383333332d333333333333333333333333000000000000002b415145424151454241514542415145424151454241514542415145424151454241514542415145424151450000000000000008000000000000000b00000000000000080000000000000016000000000000002b41674943416749434167494341674943416749434167494341674943416749434167494341674943416749000000000000001e323032362d30382d32345431353a30343a30352e3132333435363738395a000000000000001e323032362d30382d32345431353a30353a30352e3132333435363738395a")
	assertFixtureAndGolden(t, "enrollment", enrollment(t, enrollmentValue), enrollmentFixture, "ce7b67544c07a2bce3ef86276b8202af86e2c5a7f7d718ecf2bd6cf77f5162f2")

	authValue := AuthenticationBinding{ControllerID: idController, KeyID: idKey, SessionID: idSession, ClientNonce: b64(NonceBytes, 1), ServerNonce: b64(NonceBytes, 2), ACKDigest: b64(DigestBytes, 3), ExpiresAt: testTime.Add(time.Minute)}
	authFixture := fixtureTranscript("rig.relay.v1/wss-authentication", version, []byte(idController), []byte(idKey), []byte(idSession), []byte(authValue.ClientNonce), []byte(authValue.ServerNonce), []byte(authValue.ACKDigest), []byte("2026-08-24T15:05:05.123456789Z"))
	assertRawGolden(t, "wss", authFixture, "000000000000001f7269672e72656c61792e76312f7773732d61757468656e7469636174696f6e00000000000000080000000000000001000000000000002432323232323232322d323232322d343232322d383232322d323232323232323232323232000000000000002433333333333333332d333333332d343333332d383333332d333333333333333333333333000000000000002434343434343434342d343434342d343434342d383434342d343434343434343434343434000000000000002b41514542415145424151454241514542415145424151454241514542415145424151454241514542415145000000000000002b41674943416749434167494341674943416749434167494341674943416749434167494341674943416749000000000000002b41774d4441774d4441774d4441774d4441774d4441774d4441774d4441774d4441774d4441774d4441774d000000000000001e323032362d30382d32345431353a30353a30352e3132333435363738395a")
	assertFixtureAndGolden(t, "wss", authentication(t, authValue), authFixture, "b0286f220b023550a8838628720a61eddecc210e938046eef1287e55cbaeb17e")

	rotationValue := RotationProof{RotationID: idRotation, ControllerID: idController, OldKeyID: idKey, NewKeyID: idNewKey, NewPublicKey: b64(PublicKeyBytes, 1), SessionID: idSession, ServerNonce: b64(NonceBytes, 2), ExpiresAt: testTime.Add(time.Minute)}
	rotationFixture := fixtureTranscript("rig.relay.v1/key-rotation-possession", version, []byte(idRotation), []byte(idController), []byte(idKey), []byte(idNewKey), []byte(rotationValue.NewPublicKey), []byte(idSession), []byte(rotationValue.ServerNonce), []byte("2026-08-24T15:05:05.123456789Z"))
	assertRawGolden(t, "rotation", rotationFixture, "00000000000000247269672e72656c61792e76312f6b65792d726f746174696f6e2d706f7373657373696f6e00000000000000080000000000000001000000000000002438383838383838382d383838382d343838382d383838382d383838383838383838383838000000000000002432323232323232322d323232322d343232322d383232322d323232323232323232323232000000000000002433333333333333332d333333332d343333332d383333332d333333333333333333333333000000000000002439393939393939392d393939392d343939392d383939392d393939393939393939393939000000000000002b41514542415145424151454241514542415145424151454241514542415145424151454241514542415145000000000000002434343434343434342d343434342d343434342d383434342d343434343434343434343434000000000000002b41674943416749434167494341674943416749434167494341674943416749434167494341674943416749000000000000001e323032362d30382d32345431353a30353a30352e3132333435363738395a")
	assertFixtureAndGolden(t, "rotation", rotation(t, rotationValue), rotationFixture, "71ce55065316d9bd5ca80940fad9b086b79a6ea11f023b63702dc6f58fb9dc0b")

	ackState := []ACKState{{SubscriptionID: idDelivery, Generation: 7}, {SubscriptionID: idEvent, Generation: 9}}
	count := make([]byte, 8)
	binary.BigEndian.PutUint64(count, 2)
	seven := make([]byte, 8)
	binary.BigEndian.PutUint64(seven, 7)
	nine := make([]byte, 8)
	binary.BigEndian.PutUint64(nine, 9)
	ackFixture := fixtureTranscript("rig.relay.v1/ack-digest", count, []byte(idDelivery), seven, []byte(idEvent), nine)
	assertRawGolden(t, "ack", ackFixture, "00000000000000177269672e72656c61792e76312f61636b2d64696765737400000000000000080000000000000002000000000000002436363636363636362d363636362d343636362d383636362d36363636363636363636363600000000000000080000000000000007000000000000002437373737373737372d373737372d343737372d383737372d37373737373737373737373700000000000000080000000000000009")
	wantACK := sha256.Sum256(ackFixture)
	gotACK, err := CanonicalACKDigest(ackState)
	if err != nil {
		t.Fatal(err)
	}
	if gotACK != wantACK {
		t.Fatalf("ACK fixture mismatch: got %x want %x", gotACK, wantACK)
	}
	if got := hex.EncodeToString(gotACK[:]); got != "0a0de3a26fc767012a299ad7690f71a957f1a8542eb30d9e61f71caa26dc0a3b" {
		t.Fatalf("ACK golden = %s", got)
	}
}

func fixtureTranscript(domain string, fields ...[]byte) []byte {
	values := append([][]byte{[]byte(domain)}, fields...)
	var fixture []byte
	for _, value := range values {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		fixture = append(fixture, length[:]...)
		fixture = append(fixture, value...)
	}
	return fixture
}

func assertFixtureAndGolden(t *testing.T, name string, production, fixture []byte, golden string) {
	t.Helper()
	if !bytes.Equal(production, fixture) {
		t.Fatalf("%s transcript bytes differ from independent fixture\nproduction=%x\nfixture=%x", name, production, fixture)
	}
	digest := sha256.Sum256(production)
	if got := hex.EncodeToString(digest[:]); got != golden {
		t.Fatalf("%s golden = %s", name, got)
	}
}

func assertRawGolden(t *testing.T, name string, fixture []byte, golden string) {
	t.Helper()
	if got := hex.EncodeToString(fixture); got != golden {
		t.Fatalf("%s raw fixture = %s", name, got)
	}
}

func enrollment(t *testing.T, value EnrollmentProof) []byte {
	t.Helper()
	transcript, err := EnrollmentTranscript(value)
	if err != nil {
		t.Fatal(err)
	}
	return transcript
}
func authentication(t *testing.T, value AuthenticationBinding) []byte {
	t.Helper()
	transcript, err := WSSAuthenticationTranscript(value)
	if err != nil {
		t.Fatal(err)
	}
	return transcript
}
func rotation(t *testing.T, value RotationProof) []byte {
	t.Helper()
	transcript, err := KeyRotationTranscript(value)
	if err != nil {
		t.Fatal(err)
	}
	return transcript
}

func FuzzTranscriptFieldBoundaries(f *testing.F) {
	f.Add("a", "bc", "ab", "c")
	f.Fuzz(func(t *testing.T, a, b, c, d string) {
		left := buildTranscript("fuzz", []byte(a), []byte(b))
		right := buildTranscript("fuzz", []byte(c), []byte(d))
		if a == c && b == d {
			if !bytes.Equal(left, right) {
				t.Fatal("identical fields differ")
			}
		} else if bytes.Equal(left, right) {
			t.Fatal("distinct field sequences collide")
		}
	})
}
