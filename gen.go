package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strconv"
)

var (
	cenum = flag.Bool("cenum", true, "generate class enum")
	cbits = flag.Bool("cbits", true, "print class encoding table")
	defs  = flag.Bool("defs", true, "generate #define")
	fns   = flag.Bool("fns", true, "generate function stubs")
	itab  = flag.Bool("itab", true, "generate master instruction table")
	proto = flag.Bool("proto", true, "generate function prototypes")
)

// pos is the position in the file.
type pos struct {
	r       io.Reader
	line    int
	*sclass        // current superclass
	*class         // current class
	comm    string // current comment
}

func (p *pos) String() string {
	s := "???:"
	f, ok := p.r.(*os.File)
	if ok {
		s = f.Name() + ":"
	}
	return s + fmt.Sprint(p.line)
}

// bits is a set used to check if a bit should be masked or not.
// See sclass, currently unused on arm64.
type bits [32]bool

// sclass represents a coarsely-grained instruction class. Original
// purpose was to ignore super-class bits in xo calculation. It's now
// ignored on arm64.
type sclass struct {
	name string
	bits
	p *pos
}

// cre parses a field in a superclass line (@foo)
var cre = regexp.MustCompile(`^(\d+),?(\d+)?$`)

func (p *pos) updateSclass(buf []byte) {
	sc := &sclass{p: p}
	wsc := bufio.NewScanner(bytes.NewBuffer(buf))
	wsc.Split(bufio.ScanWords)
	for wsc.Scan() {
		fld := wsc.Text()
		if sc.name == "" {
			sc.name = fld[1:]
			continue
		}
		ss := cre.FindAllStringSubmatch(fld, -1)
		if len(ss) != 1 || len(ss[0]) != 3 {
			log.Fatalf(`%v: can't decode field "%s" %+q`, p, fld, ss)
		}
		bhi, _ := strconv.Atoi(ss[0][1])
		blo := bhi
		if ss[0][2] != "" {
			blo, _ = strconv.Atoi(ss[0][2])
		}
		for i := bhi; i >= blo; i-- {
			sc.bits[i] = true
		}
	}
	p.sclass = sc
}

// class represents a finely-grained instruction class.
type class struct {
	name   string
	comm   string
	p      *pos
	instrs []*instr
	x      uint64 // class index
}

func (p *pos) newClass(s string) *class {
	c := &class{name: s, comm: p.comm, p: p}
	if p.class != nil {
		c.x = (p.class.x>>6 + 1) << 6
	}
	return c
}

func (c *class) addInstr(i *instr) {
	c.instrs = append(c.instrs, i)
}

// all instruction classes found during parsing
var classes []*class

// instr represents a particular instruction form from the instruction
// encoding table.
type instr struct {
	name     string
	typ      string // Iload, Iarith, etc.
	dispatch string // cmpb, dp1, etc
	*class
	*sclass // the bits in sclass don't count in xo calculation
	fields  []field
	p       *pos
}

func (p *pos) newInstr(buf []byte) *instr {
	i := &instr{p: p, sclass: p.sclass, class: p.class}
	wsc := bufio.NewScanner(bytes.NewBuffer(buf))
	wsc.Split(bufio.ScanWords)
	for wsc.Scan() {
		fld := wsc.Text()
		if i.name == "" {
			i.name = fld
			continue
		}
		if i.dispatch == "" {
			i.dispatch = fld
			continue
		}
		if i.typ == "" {
			i.typ = fld
			continue
		}
		i.addField(fld)
	}
	return i
}

func (in *instr) xo() op {
	xo := op{p: in.p}
	for _, v := range in.fields {
		if v.ignore || !v.set || v.name == "$" {
			continue
		}
		xo.oo = xo.oo<<(v.high-v.low+1) | v.val
	}
	xo.xo = in.x | xo.oo
	return xo
}

// op is the xo of an instruction that records its position information
// so that it can print itself.
type op struct {
	xo uint64
	oo uint64
	p  *pos
}

func (xo op) String() string {
	return xo.p.String() + " " + strconv.FormatUint(xo.xo, 10) + " " + strconv.FormatUint(xo.oo, 10) + " " + xo.p.class.name + " " + strconv.FormatUint(xo.p.class.x, 10)
}

// re splits a field from the instruction encoding table.
// 	\1 matches leading minus; is "" if unset
// 	\2 matches field name; "$" represents part of an opcode
// 	\3 matches first bit position in decimal
//	\4 matches last bit position in decimal; is "" if field is 1-bit
//	\5 matches the value of the field in hexadecimal; is "" if unset
var re = regexp.MustCompile(`^(-)?(\$|\w+)<(\d+),?(\d+)?>=?([[:xdigit:]]+)?$`)

// field represents a field from the instruction encoding table.
type field struct {
	ignore    bool   // leading minus
	name      string // name in the manual or $ for unnamed fields (opcodes)
	high, low uint64 // bit positions of the field
	set       bool   // true if field has a value
	val       uint64 // meaningful only if set is true
}

// addField adds a field to instruction i decoding s using re.
func (i *instr) addField(s string) {
	ss := re.FindAllStringSubmatch(s, -1)
	if len(ss) != 1 || len(ss[0]) != 6 {
		log.Fatalf(`%v: can't decode field "%s"`, i.p, s)
	}
	var f field
	if ss[0][1] == "-" {
		f.ignore = true
	}
	f.name = ss[0][2]
	f.high, _ = strconv.ParseUint(ss[0][3], 10, 0)
	if ss[0][4] == "" {
		f.low = f.high
	} else {
		f.low, _ = strconv.ParseUint(ss[0][4], 10, 0)
	}
	if ss[0][5] == "" {
		i.fields = append(i.fields, f)
		return
	}
	f.set = true
	v, err := strconv.ParseUint(ss[0][5], 16, 0)
	if err != nil {
		log.Fatalf(`can't decode field "%s": %v`, s, err)
	}
	f.val = v
	i.fields = append(i.fields, f)
}

func init() {
	log.SetPrefix("")
	log.SetFlags(0)
	flag.Usage = usage
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: dups [options] [file]\n")
	flag.PrintDefaults()
}

func gencenum() {
	fmt.Println("/* instruction classes: iota << 6. */")
	fmt.Println("enum {")
	for _, v := range classes {
		fmt.Printf("\t%-6s = %4d,\t/*%s */\n", "C"+v.name, v.x, v.comm)
	}
	fmt.Println("};")
}

func genitab() {
	fmt.Println("Inst itab[] =")
	fmt.Println("{")
	for _, c := range classes {
		fmt.Printf("\t/*%s */\n", c.comm)
		for _, i := range c.instrs {
			xo := i.xo()
			v := fmt.Sprintf("[C%s+%2d]", i.class.name, xo.oo)
			fmt.Printf("\t%s\t", v)
			fmt.Printf("{%s, %-9s\t%6s}, /* %d */\n", i.dispatch, `"`+i.name+`",`, i.typ, xo.xo)
		}
		fmt.Println("")
	}
	fmt.Println("\t{ 0 }")
	fmt.Println("};")
}

func genfncomm(in *instr) {
	fmt.Printf("\n/*%s\n", in.class.comm)
	fmt.Printf("params: ")
	for _, f := range in.fields {
		if f.ignore || f.name == "$" || f.set {
			continue
		}
		if f.high == f.low {
			fmt.Printf("%s<%d> ", f.name, f.high)
		} else {
			fmt.Printf("%s<%d,%d> ", f.name, f.high, f.low)
		}
	}
	fmt.Printf("\nops: ")
	for _, f := range in.fields {
		if f.ignore || f.name == "$" || !f.set {
			continue
		}
		if f.high == f.low {
			fmt.Printf("%s<%d> ", f.name, f.high)
		} else {
			fmt.Printf("%s<%d,%d> ", f.name, f.high, f.low)
		}
	}
	fmt.Printf("\n")
	for _, i := range in.class.instrs {
		if i.dispatch != in.dispatch {
			continue
		}
		fmt.Printf("\t%-6s\t", i.name)
		for _, f := range i.fields {
			if f.ignore || f.name == "$" || !f.set {
				continue
			}
			fmt.Printf("%s=%d\t", f.name, f.val)
		}
		fmt.Printf("\n")
	}
	fmt.Printf("*/\n")
}

func genfns() {
	for _, v := range classes {
		fns := map[string]bool{} // set of functions in a class
		for _, i := range v.instrs {
			if fns[i.dispatch] {
				continue
			}
			fns[i.dispatch] = true
			genfncomm(i)
			fmt.Printf("void\n%s(ulong ir)\n{\n", i.dispatch)
			// generate variables
			fmt.Printf("\tulong ")
			needcomma := false
			for _, f := range i.fields {
				if f.ignore || f.name == "$" {
					continue
				}

				if needcomma {
					fmt.Printf(", ")
				}
				fmt.Printf("%s", f.name)
				needcomma = true
			}
			fmt.Printf(";\n\n")
			// get variables
			fmt.Printf("\tget%s(ir);\n", i.class.name)
			// use variables
			fmt.Printf("\tUSED(")
			needcomma = false
			for _, f := range i.fields {
				if f.ignore || f.name == "$" {
					continue
				}
				if !f.set {
					continue
				}

				if needcomma {
					fmt.Printf(", ")
				}
				fmt.Printf("%s", f.name)
				needcomma = true
			}
			fmt.Printf(");\n")
			// print trace
			fmt.Printf("\tif(trace)\n")
			fmt.Printf("\t\titrace(\"%%s\\t")
			needcomma = false
			for _, f := range i.fields {
				if f.ignore || f.name == "$" || f.set {
					continue
				}
				if needcomma {
					fmt.Printf(", ")
				}
				fmt.Printf("%s=%%d", f.name)
				needcomma = true
			}
			fmt.Printf(`", ci->name`)
			for _, f := range i.fields {
				if f.ignore || f.name == "$" || f.set {
					continue
				}
				fmt.Printf(", %s", f.name)
			}
			fmt.Printf(");\n")
			fmt.Printf("}\n")
		}
	}
}

func gengetvars() {
	for _, v := range classes {
		i := v.instrs[0] // first instuction is enough
		fmt.Printf("#define %-11s\t", "get" + i.class.name + "(i)")
		for _, f := range i.fields {
			if f.ignore || f.name == "$" {
				continue
			}
			bits := f.high - f.low + 1
			mask := 1<<bits - 1
			if f.low != 0 {
				fmt.Printf("%s = (i>>%d)&0x%x; ", f.name, f.low, mask)
			} else {
				fmt.Printf("%s = i&0x%x; ", f.name, mask)
			}
		}
		fmt.Printf("\n")
	}
}

func gengetops() {
	for _, v := range classes {
		in := v.instrs[0] // first instuction is enough
		fmt.Printf("#define %-10s\t(", "op" + in.class.name + "(i)")
		var pos uint64
		var needpipe = false
		for i := len(in.fields) - 1; i >= 0; i-- {
			f := in.fields[i]
			if f.ignore || f.name == "$" || !f.set {
				continue
			}
			bits := f.high - f.low + 1
			mask := 1<<bits - 1
			if needpipe {
				fmt.Printf("|")
			}
			if pos == 0 {
				if f.low != 0 {
					fmt.Printf("(i>>%d)&0x%x", f.low, mask)
				} else {
					fmt.Printf("i&0x%x", mask)
				}
			} else {
				if f.low != 0 {
					fmt.Printf("(((i>>%d)&0x%x)<<%d)", f.low, mask, pos)
				} else {
					fmt.Printf("((i&0x%x)<<%d)", mask, pos)
				}
			}
			pos += bits
			needpipe = true
		}
		fmt.Printf(")\n")
	}
}

func gendefs() {
	gengetvars()
	gengetops()
}

// byOpLen implements sort.Interface for []*class, sorting by the
// length of the class opcode fields.
type byOpLen []*class

func (c byOpLen) Len() int      { return len(c) }
func (c byOpLen) Swap(i, j int) { c[i], c[j] = c[j], c[i] }

func (c byOpLen) Less(i, j int) bool {
	ii := c[i].instrs[0] // first instuction is enough
	ji := c[j].instrs[0]
	var opilen, opjlen uint64
	for _, f := range ii.fields {
		if f.name == "$" {
			opilen += f.high - f.low + 1
		}
	}
	for _, f := range ji.fields {
		if f.name == "$" {
			opjlen += f.high - f.low + 1
		}
	}
	return opilen < opjlen
}

// opBits implements stringer allowing for concise printing of the
// opcode bits.
type opBits []*class

func (cs opBits) String() string {
	s := `/* Font
Instruction encoding table, per class. Bullets represent bits which
select a particular instruction from a particular instruction class.

`
	for _, v := range cs {
		in := v.instrs[0] // first instuction is enough
		var b bits
		var val uint64
		for _, f := range in.fields {
			if f.name != "$" {
				if f.ignore == false && f.set {
					val |= (1<<(f.high-f.low+1) - 1) << f.low
				}
				continue
			}
			val |= f.val << f.low
			for i := f.high; i >= f.low; i-- {
				b[i] = true
			}
		}
		for i := 31; i >= 0; i-- {
			if i%4 == 3 && i != 31 {
				s += fmt.Sprintf("|")
			}
			if !b[i] {
				if (val>>uint64(i))&1 == 1 {
					s += fmt.Sprintf("•")
				} else {
					s += fmt.Sprintf(".")
				}
				continue
			}
			s += fmt.Sprintf("%d", (val>>uint64(i))&1)
		}
		s += fmt.Sprintf(" %-5s%s\n", v.name, v.comm)
	}
	return s + "\n*/"
}

func gencbits() {
	fmt.Printf("%v\n", opBits(classes))
	return
}

func genproto() {
	for _, v := range classes {
		fns := map[string]bool{} // set of functions in a class
		for _, i := range v.instrs {
			if fns[i.dispatch] {
				continue
			}
			fns[i.dispatch] = true
			fmt.Printf("void\t%s(ulong);\n", i.dispatch)
		}
	}
}

func main() {
	flag.Parse()

	var err error
	var f *os.File
	var lsc *bufio.Scanner
	switch flag.NArg() {
	case 0:
		f = os.Stdin
	case 1:
		f, err = os.Open(flag.Arg(0))
		if err != nil {
			log.Fatalf("can't open %v: %v", flag.Arg(0), err)
		}
	default:
		usage()
		os.Exit(1)
	}
	lsc = bufio.NewScanner(f)
	p := &pos{r: f, line: 0}
	var cc *class // current class
	for lsc.Scan() {
		p.line++
		b := lsc.Bytes()
		if len(b) == 0 {
			continue
		}
		switch b[0] {
		case '#', '!', '@':
			switch b[0] {
			case '#':
				p.comm = string(b[1:])
			case '!':
				cc = p.newClass(string(b[1:]))
				classes = append(classes, cc)
				p.class = cc
			case '@':
				p.updateSclass(b)
			}
			continue
		}
		instr := p.newInstr(b)
		cc.addInstr(instr)
		// xo := instr.xo()
		// fmt.Printf("%v %d %d\n", instr.p, xo.xo, xo.oo)
	}
	if err := lsc.Err(); err != nil {
		log.Fatalf("error scanning: %v", err)
	}
	if *cenum {
		gencenum()
	}
	if *itab {
		genitab()
	}
	if *fns {
		genfns()
	}
	if *defs {
		gendefs()
	}
	if *cbits {
		gencbits()
	}
	if *proto {
		genproto()
	}
}
