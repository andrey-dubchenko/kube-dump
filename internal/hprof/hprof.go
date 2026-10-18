package hprof

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"kube-dump/internal/size"
)

// HPROF record and sub-record tags (JDK heap dump format, see hprof_b_spec.h).
const (
	hprofUTF8        = 0x01
	hprofLoadClass   = 0x02
	hprofHeapDump    = 0x0C
	hprofHeapSegment = 0x1C

	subRootUnknown     = 0xFF
	subRootJNIGlobal   = 0x01
	subRootJNILocal    = 0x02
	subRootJavaFrame   = 0x03
	subRootNativeStack = 0x04
	subRootSticky      = 0x05
	subRootThreadBlock = 0x06
	subRootMonitor     = 0x07
	subRootThreadObj   = 0x08
	subClassDump       = 0x20
	subInstanceDump    = 0x21
	subObjArrayDump    = 0x22
	subPrimArrayDump   = 0x23
	subRootInterned    = 0x89
	subRootFinalizing  = 0x8A
	subRootDebugger    = 0x8B
	subRootVMInternal  = 0x8D
	subRootJNIMonitor  = 0x8E
	subHeapDumpInfo    = 0xFE
)

// primKeyBit marks histogram keys that stand for a primitive array type, not a class id.
const primKeyBit = uint64(1) << 63

var primNames = map[byte]string{4: "boolean", 5: "char", 6: "float", 7: "double", 8: "byte", 9: "short", 10: "int", 11: "long"}

// stat is the histogram entry of one class.
type stat struct {
	count int64
	bytes int64
}

// array is one of the largest single arrays.
type array struct {
	bytes  int64
	length int64
	key    uint64
}

// Summary is what a streaming pass over a heap dump yields: a class histogram that
// is exact in counts and approximate in size (object headers are not in the dump).
type Summary struct {
	Version    string
	IDSize     int
	Time       time.Time
	Instances  int64
	Arrays     int64
	Classes    int
	TotalBytes int64

	hist     map[uint64]*stat
	classes  map[uint64]uint64 // class object id -> class name string id
	strings  map[uint64]string
	top      []array
	topLimit int
}

// reader reads big-endian HPROF values from a stream and counts the bytes consumed.
type reader struct {
	r      *bufio.Reader
	idSize int
	pos    int64
	buf    [8]byte
}

func (h *reader) fill(n int) error {
	if _, err := io.ReadFull(h.r, h.buf[:n]); err != nil {
		return err
	}
	h.pos += int64(n)
	return nil
}

func (h *reader) u1() (byte, error) {
	if err := h.fill(1); err != nil {
		return 0, err
	}
	return h.buf[0], nil
}

func (h *reader) u2() (uint16, error) {
	if err := h.fill(2); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(h.buf[:2]), nil
}

func (h *reader) u4() (uint32, error) {
	if err := h.fill(4); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(h.buf[:4]), nil
}

func (h *reader) u8() (uint64, error) {
	if err := h.fill(8); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(h.buf[:8]), nil
}

func (h *reader) id() (uint64, error) {
	if h.idSize == 4 {
		v, err := h.u4()
		return uint64(v), err
	}
	return h.u8()
}

func (h *reader) skip(n int64) error {
	for n > 0 {
		step := n
		if step > 1<<30 {
			step = 1 << 30
		}
		d, err := h.r.Discard(int(step))
		h.pos += int64(d)
		if err != nil {
			return err
		}
		n -= step
	}
	return nil
}

func (h *reader) bytes(n int64) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(h.r, b); err != nil {
		return nil, err
	}
	h.pos += n
	return b, nil
}

// valueSize is the size in the dump of a value of the given HPROF basic type.
func (h *reader) valueSize(t byte) (int64, error) {
	switch t {
	case 2:
		return int64(h.idSize), nil
	case 4, 8:
		return 1, nil
	case 5, 9:
		return 2, nil
	case 6, 10:
		return 4, nil
	case 7, 11:
		return 8, nil
	}
	return 0, fmt.Errorf("unknown value type %d", t)
}

// Analyze makes one streaming pass over a heap dump; memory use does not depend on
// the dump size (it grows with the number of classes only). top is how many of the largest
// arrays to remember.
func Analyze(in io.Reader, top int) (*Summary, error) {
	h := &reader{r: bufio.NewReaderSize(in, 1<<20)}
	s := &Summary{
		hist:     map[uint64]*stat{},
		classes:  map[uint64]uint64{},
		strings:  map[uint64]string{},
		topLimit: top,
	}

	version, err := h.r.ReadString(0)
	if err != nil || !strings.HasPrefix(version, "JAVA PROFILE") {
		return nil, errors.New("not an HPROF heap dump (missing JAVA PROFILE header)")
	}
	h.pos += int64(len(version))
	s.Version = strings.TrimSuffix(version, "\x00")
	idSize, err := h.u4()
	if err != nil || (idSize != 4 && idSize != 8) {
		return nil, fmt.Errorf("unsupported identifier size %d", idSize)
	}
	h.idSize, s.IDSize = int(idSize), int(idSize)
	ms, err := h.u8()
	if err != nil {
		return nil, err
	}
	s.Time = time.UnixMilli(int64(ms))

	for {
		tag, err := h.u1()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if _, err := h.u4(); err != nil { // time offset
			return nil, err
		}
		length, err := h.u4()
		if err != nil {
			return nil, err
		}
		switch tag {
		case hprofUTF8:
			if err := s.readUTF8(h, int64(length)); err != nil {
				return nil, err
			}
		case hprofLoadClass:
			if err := s.readLoadClass(h); err != nil {
				return nil, err
			}
		case hprofHeapDump, hprofHeapSegment:
			if err := s.readHeapSegment(h, int64(length)); err != nil {
				return nil, fmt.Errorf("heap dump at byte %d: %w", h.pos, err)
			}
		default:
			if err := h.skip(int64(length)); err != nil {
				return nil, err
			}
		}
	}
	s.Classes = len(s.classes)
	return s, nil
}

func (s *Summary) readUTF8(h *reader, length int64) error {
	id, err := h.id()
	if err != nil {
		return err
	}
	n := length - int64(h.idSize)
	if n < 0 || n > 1<<20 { // class and field names are short; skip anything odd
		return h.skip(max(n, 0))
	}
	b, err := h.bytes(n)
	if err != nil {
		return err
	}
	s.strings[id] = string(b)
	return nil
}

func (s *Summary) readLoadClass(h *reader) error {
	if _, err := h.u4(); err != nil { // class serial
		return err
	}
	classID, err := h.id()
	if err != nil {
		return err
	}
	if _, err := h.u4(); err != nil { // stack trace serial
		return err
	}
	nameID, err := h.id()
	if err != nil {
		return err
	}
	s.classes[classID] = nameID
	return nil
}

func (s *Summary) add(key uint64, bytes int64) {
	st := s.hist[key]
	if st == nil {
		st = &stat{}
		s.hist[key] = st
	}
	st.count++
	st.bytes += bytes
	s.TotalBytes += bytes
}

func (s *Summary) noteArray(key uint64, bytes, length int64) {
	if s.topLimit <= 0 {
		return
	}
	if len(s.top) == s.topLimit && bytes <= s.top[len(s.top)-1].bytes {
		return
	}
	s.top = append(s.top, array{bytes: bytes, length: length, key: key})
	sort.Slice(s.top, func(i, j int) bool { return s.top[i].bytes > s.top[j].bytes })
	if len(s.top) > s.topLimit {
		s.top = s.top[:s.topLimit]
	}
}

// readHeapSegment walks the sub-records of one HEAP_DUMP / HEAP_DUMP_SEGMENT record.
func (s *Summary) readHeapSegment(h *reader, length int64) error {
	end := h.pos + length
	idSize := int64(h.idSize)
	for h.pos < end {
		tag, err := h.u1()
		if err != nil {
			return err
		}
		switch tag {
		case subRootUnknown, subRootSticky, subRootMonitor, subRootInterned, subRootFinalizing, subRootDebugger, subRootVMInternal:
			err = h.skip(idSize)
		case subRootJNIGlobal:
			err = h.skip(2 * idSize)
		case subRootJNILocal, subRootJavaFrame, subRootThreadObj, subRootJNIMonitor:
			err = h.skip(idSize + 8)
		case subRootNativeStack, subRootThreadBlock:
			err = h.skip(idSize + 4)
		case subHeapDumpInfo:
			err = h.skip(4 + idSize)
		case subClassDump:
			err = s.skipClassDump(h)
		case subInstanceDump:
			err = s.readInstance(h)
		case subObjArrayDump:
			err = s.readObjArray(h)
		case subPrimArrayDump:
			err = s.readPrimArray(h)
		default:
			return fmt.Errorf("unknown heap sub-record 0x%02x", tag)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Summary) skipClassDump(h *reader) error {
	// class id, stack serial, super, loader, signers, protection domain, 2 reserved ids, instance size
	if err := h.skip(int64(h.idSize) + 4 + 6*int64(h.idSize) + 4); err != nil {
		return err
	}
	cp, err := h.u2()
	if err != nil {
		return err
	}
	for i := 0; i < int(cp); i++ {
		if err := h.skip(2); err != nil { // constant pool index
			return err
		}
		t, err := h.u1()
		if err != nil {
			return err
		}
		sz, err := h.valueSize(t)
		if err != nil {
			return err
		}
		if err := h.skip(sz); err != nil {
			return err
		}
	}
	statics, err := h.u2()
	if err != nil {
		return err
	}
	for i := 0; i < int(statics); i++ {
		if err := h.skip(int64(h.idSize)); err != nil { // name
			return err
		}
		t, err := h.u1()
		if err != nil {
			return err
		}
		sz, err := h.valueSize(t)
		if err != nil {
			return err
		}
		if err := h.skip(sz); err != nil {
			return err
		}
	}
	fields, err := h.u2()
	if err != nil {
		return err
	}
	return h.skip(int64(fields) * (int64(h.idSize) + 1))
}

func (s *Summary) readInstance(h *reader) error {
	if err := h.skip(int64(h.idSize) + 4); err != nil { // object id, stack serial
		return err
	}
	class, err := h.id()
	if err != nil {
		return err
	}
	n, err := h.u4()
	if err != nil {
		return err
	}
	s.Instances++
	s.add(class, int64(n))
	return h.skip(int64(n))
}

func (s *Summary) readObjArray(h *reader) error {
	if err := h.skip(int64(h.idSize) + 4); err != nil {
		return err
	}
	n, err := h.u4()
	if err != nil {
		return err
	}
	class, err := h.id()
	if err != nil {
		return err
	}
	bytes := int64(n) * int64(h.idSize)
	s.Arrays++
	s.add(class, bytes)
	s.noteArray(class, bytes, int64(n))
	return h.skip(bytes)
}

func (s *Summary) readPrimArray(h *reader) error {
	if err := h.skip(int64(h.idSize) + 4); err != nil {
		return err
	}
	n, err := h.u4()
	if err != nil {
		return err
	}
	t, err := h.u1()
	if err != nil {
		return err
	}
	sz, err := h.valueSize(t)
	if err != nil {
		return err
	}
	bytes := int64(n) * sz
	key := primKeyBit | uint64(t)
	s.Arrays++
	s.add(key, bytes)
	s.noteArray(key, bytes, int64(n))
	return h.skip(bytes)
}

// className turns a histogram key into a readable Java name.
func (s *Summary) className(key uint64) string {
	if key&primKeyBit != 0 {
		return primNames[byte(key&0xFF)] + "[]"
	}
	nameID, ok := s.classes[key]
	if !ok {
		return fmt.Sprintf("<class 0x%x>", key)
	}
	return JavaTypeName(s.strings[nameID])
}

// JavaTypeName converts HPROF/JVM names: java/lang/String -> java.lang.String,
// [Ljava/lang/Object; -> java.lang.Object[], [[I -> int[][].
func JavaTypeName(n string) string {
	dims := 0
	for strings.HasPrefix(n, "[") {
		dims++
		n = n[1:]
	}
	if dims > 0 {
		switch {
		case strings.HasPrefix(n, "L") && strings.HasSuffix(n, ";"):
			n = n[1 : len(n)-1]
		case len(n) == 1:
			names := map[string]string{"Z": "boolean", "C": "char", "F": "float", "D": "double", "B": "byte", "S": "short", "I": "int", "J": "long"}
			if v, ok := names[n]; ok {
				n = v
			}
		}
	}
	return strings.ReplaceAll(n, "/", ".") + strings.Repeat("[]", dims)
}

// Row is one line of the histogram: a class (or primitive array type) with its totals.
type Row struct {
	Name  string
	Count int64
	Bytes int64
}

// Rows returns the whole histogram, in no particular order.
func (s *Summary) Rows() []Row {
	rows := make([]Row, 0, len(s.hist))
	for key, st := range s.hist {
		rows = append(rows, Row{s.className(key), st.count, st.bytes})
	}
	return rows
}

// ArrayRow is one of the largest single arrays.
type ArrayRow struct {
	Name   string
	Bytes  int64
	Length int64
}

// TopArrays returns the largest single arrays, biggest first.
func (s *Summary) TopArrays() []ArrayRow {
	out := make([]ArrayRow, 0, len(s.top))
	for _, a := range s.top {
		out = append(out, ArrayRow{s.className(a.key), a.bytes, a.length})
	}
	return out
}

func (s *Summary) header() string {
	var b strings.Builder
	objects := s.Instances + s.Arrays
	fmt.Fprintf(&b, "HPROF summary (%s, id size %d, dump time %s)\n", s.Version, s.IDSize, s.Time.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "objects: %d (%d instances, %d arrays), classes: %d, data: %s\n", objects, s.Instances, s.Arrays, s.Classes, size.KiB(s.TotalBytes/1024))
	b.WriteString("sizes are the field and array data in the dump: object headers are not counted, object references use the dump identifier size\n")
	return b.String()
}

// table renders the top rows ordered by less.
func (s *Summary) table(title string, top int, less func(a, b Row) bool) string {
	rows := s.Rows()
	sort.Slice(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s\n  %12s  %12s  %5s  %s\n", title, "instances", "size", "%", "class")
	for i, r := range rows {
		if i >= top {
			break
		}
		pct := 0.0
		if s.TotalBytes > 0 {
			pct = 100 * float64(r.Bytes) / float64(s.TotalBytes)
		}
		fmt.Fprintf(&b, "  %12d  %12s  %5.1f  %s\n", r.Count, size.KiB(r.Bytes/1024), pct, r.Name)
	}
	return b.String()
}

func (s *Summary) bySize(top int) string {
	return s.table(fmt.Sprintf("Top %d classes by size", top), top, func(a, b Row) bool {
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		return a.Name < b.Name
	})
}

// FormatBrief renders the header and the top classes by size only.
func (s *Summary) FormatBrief(top int) string {
	return s.header() + s.bySize(top)
}

// Format renders the summary as plain text: the top classes by size and by instance count
// and the largest single arrays.
func (s *Summary) Format(top int) string {
	var b strings.Builder
	b.WriteString(s.header())
	b.WriteString(s.bySize(top))
	b.WriteString(s.table(fmt.Sprintf("Top %d classes by instance count", top), top, func(a, b Row) bool {
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Name < b.Name
	}))
	if len(s.top) > 0 {
		fmt.Fprintf(&b, "\nLargest arrays\n  %12s  %12s  %s\n", "size", "length", "type")
		for _, a := range s.top {
			fmt.Fprintf(&b, "  %12s  %12d  %s\n", size.KiB(a.bytes/1024), a.length, s.className(a.key))
		}
	}
	return b.String()
}

// AnalyzeFile summarizes a heap dump file.
func AnalyzeFile(path string, top int) (*Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Analyze(f, top)
}
