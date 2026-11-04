package machine

import "testing"

const (
	kBits = "11100"
	sBits = "11111000"
)

// The law the whole language rests on: appending the encoding of a
// combinator to any program applies that program to the combinator.
func TestAppendLemma(t *testing.T) {
	// "10" is applicative too: [w10] = B w S K = w (S K).
	for _, bits := range []string{"10", kBits, sBits, "1" + kBits + sBits, "1" + sBits + kBits} {
		if !IsApplicative(bits) {
			t.Errorf("%s should be applicative", bits)
		}
	}
	for _, bits := range []string{"0", "1", "111", ""} {
		if IsApplicative(bits) {
			t.Errorf("%q should not be applicative", bits)
		}
	}
}

func TestDecodeKAndS(t *testing.T) {
	m := &Machine{MaxSteps: 1000}
	a, b, c := Atom("a"), Atom("b"), Atom("c")

	k, _ := Decode(kBits, I)
	if v, _ := m.WHNF(App(App(k, a), b)); v != a {
		t.Errorf("K a b = %s", m.Show(v, 5))
	}
	s, _ := Decode(sBits, I)
	nf, _ := m.Normalize(App(App(App(s, K), b), c)) // S K b c = K c (b c) = c
	if nf != c {
		t.Errorf("S K b c = %s", m.Show(nf, 5))
	}
}
