package hprof_test

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"kube-dump/internal/hprof"
)

// HPROF tags used by the synthetic dump (JDK heap dump format).
const (
	tagUTF8      = 0x01
	tagLoadClass = 0x02
	tagSegment   = 0x1C
	tagEnd       = 0x2C

	subRootUnknown  = 0xFF
	subRootJNILocal = 0x02
	subClassDump    = 0x20
	subInstanceDump = 0x21
	subObjArray     = 0x22
	subPrimArray    = 0x23
)

// hb builds a small synthetic HPROF file.
type hb struct {
	idSize int
	out    bytes.Buffer
}

func (b *hb) u1(v byte)   { b.out.WriteByte(v) }
func (b *hb) u2(v uint16) { binary.Write(&b.out, binary.BigEndian, v) }
func (b *hb) u4(v uint32) { binary.Write(&b.out, binary.BigEndian, v) }
func (b *hb) u8(v uint64) { binary.Write(&b.out, binary.BigEndian, v) }
func (b *hb) id(v uint64) {
	if b.idSize == 4 {
		b.u4(uint32(v))
	} else {
		b.u8(v)
	}
}

func (b *hb) record(tag byte, body []byte) {
	b.u1(tag)
	b.u4(0)
	b.u4(uint32(len(body)))
	b.out.Write(body)
}

// sub builds a record body with the same id size.
func (b *hb) sub() *hb { return &hb{idSize: b.idSize} }

func buildHprof(idSize int) []byte {
	b := &hb{idSize: idSize}
	b.out.WriteString("JAVA PROFILE 1.0.2\x00")
	b.u4(uint32(idSize))
	b.u8(1760000000000)

	names := map[uint64]string{1: "java/lang/String", 2: "[Ljava/lang/Object;", 3: "com/acme/Foo"}
	for id := uint64(1); id <= 3; id++ {
		u := b.sub()
		u.id(id)
		u.out.WriteString(names[id])
		b.record(tagUTF8, u.out.Bytes())
	}
	for i, classID := range []uint64{101, 102, 103} {
		l := b.sub()
		l.u4(uint32(i + 1))
		l.id(classID)
		l.u4(0)
		l.id(uint64(i + 1)) // class 101 = String, 102 = Object[], 103 = Foo
		b.record(tagLoadClass, l.out.Bytes())
	}

	h := b.sub()
	h.u1(subRootUnknown)
	h.id(7)
	h.u1(subRootJNILocal)
	h.id(8)
	h.u4(1)
	h.u4(2)
	// CLASS_DUMP for Foo with one constant, one static and two instance fields
	h.u1(subClassDump)
	h.id(103)
	h.u4(0)
	for i := 0; i < 6; i++ {
		h.id(0)
	}
	h.u4(24)
	h.u2(1)
	h.u2(0)
	h.u1(10)
	h.u4(42)
	h.u2(1)
	h.id(5)
	h.u1(4)
	h.u1(1)
	h.u2(2)
	h.id(6)
	h.u1(10)
	h.id(7)
	h.u1(2)
	instance := func(class uint64, n int) {
		h.u1(subInstanceDump)
		h.id(1000)
		h.u4(0)
		h.id(class)
		h.u4(uint32(n))
		h.out.Write(make([]byte, n))
	}
	for i := 0; i < 3; i++ {
		instance(103, 24)
	}
	for i := 0; i < 2; i++ {
		instance(101, 16)
	}
	h.u1(subObjArray)
	h.id(2000)
	h.u4(0)
	h.u4(5)
	h.id(102)
	h.out.Write(make([]byte, 5*idSize))
	prim := func(n int, typ byte, size int) {
		h.u1(subPrimArray)
		h.id(3000)
		h.u4(0)
		h.u4(uint32(n))
		h.u1(typ)
		h.out.Write(make([]byte, n*size))
	}
	prim(1000, 8, 1)
	prim(10, 5, 2)
	b.record(tagSegment, h.out.Bytes())
	b.record(tagEnd, nil)
	return b.out.Bytes()
}

func TestAnalyzeHistogram(t *testing.T) {
	for _, idSize := range []int{8, 4} {
		sum, err := hprof.Analyze(bytes.NewReader(buildHprof(idSize)), 10)
		if err != nil {
			t.Fatalf("id size %d: %v", idSize, err)
		}
		if sum.IDSize != idSize || sum.Version != "JAVA PROFILE 1.0.2" || sum.Instances != 5 || sum.Arrays != 3 || sum.Classes != 3 {
			t.Errorf("id size %d: header/counts wrong: %+v", idSize, sum)
		}
		want := map[string][2]int64{
			"com.acme.Foo":       {3, 72},
			"java.lang.String":   {2, 32},
			"java.lang.Object[]": {1, int64(5 * idSize)},
			"byte[]":             {1, 1000},
			"char[]":             {1, 20},
		}
		got := map[string][2]int64{}
		for _, r := range sum.Rows() {
			got[r.Name] = [2]int64{r.Count, r.Bytes}
		}
		for name, w := range want {
			if got[name] != w {
				t.Errorf("id size %d: %s = %v, want %v", idSize, name, got[name], w)
			}
		}
		arrays := sum.TopArrays()
		if len(arrays) == 0 || arrays[0].Name != "byte[]" || arrays[0].Length != 1000 {
			t.Errorf("id size %d: largest array wrong: %+v", idSize, arrays)
		}
	}
}

func TestFormat(t *testing.T) {
	sum, err := hprof.Analyze(bytes.NewReader(buildHprof(8)), 3)
	if err != nil {
		t.Fatal(err)
	}
	text := sum.Format(3)
	for _, want := range []string{"JAVA PROFILE 1.0.2", "Top 3 classes by size", "Top 3 classes by instance count", "Largest arrays", "byte[]", "com.acme.Foo"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary lacks %q:\n%s", want, text)
		}
	}
	if i := strings.Index(text, "classes by size"); !strings.Contains(text[i:i+200], "byte[]") {
		t.Errorf("the biggest class must come first:\n%s", text)
	}
}

func TestFormatBriefHasOnlyTheSizeTable(t *testing.T) {
	sum, err := hprof.Analyze(bytes.NewReader(buildHprof(8)), 3)
	if err != nil {
		t.Fatal(err)
	}
	brief := sum.FormatBrief(3)
	if !strings.Contains(brief, "classes by size") || strings.Contains(brief, "by instance count") || strings.Contains(brief, "Largest arrays") {
		t.Errorf("unexpected brief summary:\n%s", brief)
	}
}

func TestAnalyzeRejectsBadInput(t *testing.T) {
	if _, err := hprof.Analyze(strings.NewReader("hello world"), 5); err == nil {
		t.Error("a text file must be rejected")
	}
	good := buildHprof(8)
	if _, err := hprof.Analyze(bytes.NewReader(good[:len(good)-40]), 5); err == nil {
		t.Error("a truncated dump must give an error, not a silent partial result")
	}
}

func TestJavaTypeName(t *testing.T) {
	cases := map[string]string{
		"java/lang/String":     "java.lang.String",
		"[Ljava/lang/Object;":  "java.lang.Object[]",
		"[[I":                  "int[][]",
		"[B":                   "byte[]",
		"com/acme/Outer$Inner": "com.acme.Outer$Inner",
	}
	for in, want := range cases {
		if got := hprof.JavaTypeName(in); got != want {
			t.Errorf("JavaTypeName(%q) = %q, want %q", in, got, want)
		}
	}
}
