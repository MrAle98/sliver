package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
)

func format(str string) string {
	if !strings.Contains(str, "_") {
		return str
	}

	str = strings.ReplaceAll(strings.ToLower(str), "_", " ")
	str = strings.ReplaceAll(strings.Title(str), " ", "")
	str = strings.ReplaceAll(str, "Crlf", "CRLF")
	str = strings.ReplaceAll(str, "Http", "HTTP")

	return str
}

func genFile(
	pkg string,
	cache map[string]string,
	lines [][]string,
) error {
	var e error
	var file string = "generated.go"
	var g *os.File
	var sorted []string

	if g, e = os.Create(file); e != nil {
		return fmt.Errorf("failed to create %s: %w", file, e)
	}
	defer g.Close()

	g.WriteString(
		"// Code generated by tools/defines.go; DO NOT EDIT.\n",
	)
	g.WriteString("package " + pkg + "\n\n")
	g.WriteString("const (\n")

	for k := range cache {
		sorted = append(sorted, k)
	}

	sort.Slice(
		sorted,
		func(i int, j int) bool {
			return len(sorted[i]) > len(sorted[j])
		},
	)

	for _, line := range lines {
		for _, k := range sorted {
			line[1] = strings.ReplaceAll(line[1], k, cache[k])
		}

		if strings.HasPrefix(line[1], "\"") {
			g.WriteString(
				fmt.Sprintf("\t%s string = %s\n", line[0], line[1]),
			)
		} else if strings.HasPrefix(line[1], "L\"") {
			g.WriteString(
				fmt.Sprintf(
					"\t%s string = %s\n",
					line[0],
					strings.Replace(line[1], "L", "", 1),
				),
			)
		} else if strings.HasPrefix(line[1], "TEXT(") {
			line[1] = strings.Replace(line[1], "TEXT(", "", 1)
			line[1] = strings.Replace(line[1], ")", "", 1)

			g.WriteString(
				fmt.Sprintf("\t%s string = %s\n", line[0], line[1]),
			)
		} else {
			g.WriteString(
				fmt.Sprintf("\t%s uintptr = %s\n", line[0], line[1]),
			)
		}
	}

	g.WriteString(")\n")

	return nil
}

func init() {
	flag.Parse()
}

func main() {
	var cache = map[string]string{
		"NULL":   "0",
		"sizeof": "len",
	}
	var lines [][]string

	if flag.NArg() == 0 {
		return
	}

	for i, arg := range flag.Args() {
		if (i == 0) || !DoesExist(arg) {
			continue
		}

		if e := processFile(&cache, &lines, arg); e != nil {
			panic(e)
		}
	}

	if e := genFile(flag.Arg(0), cache, lines); e != nil {
		panic(e)
	}
}

func processFile(
	cache *map[string]string,
	lines *[][]string,
	file string,
) error {
	var b []byte
	var e error
	var f *os.File
	var fullLine string
	var tmp []string

	if f, e = os.Open(ExpandPath(file)); e != nil {
		return fmt.Errorf("failed to open %s: %w", file, e)
	}
	defer f.Close()

	if b, e = ioutil.ReadAll(f); e != nil {
		return fmt.Errorf("failed to read %s: %w", file, e)
	}

	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)

		if strings.HasSuffix(line, "\\") {
			fullLine += line[:len(line)-1]
			continue
		} else {
			fullLine += line
		}

		line = fullLine
		fullLine = ""

		if !strings.HasPrefix(line, "#define") {
			continue
		}

		line = strings.Replace(line, "#define ", "", 1)
		tmp = strings.SplitN(line, " ", 2)

		if (len(tmp) != 2) ||
			strings.Contains(tmp[0], "(") ||
			strings.Contains(tmp[0], ")") ||
			strings.Contains(tmp[1], "~") ||
			strings.Contains(tmp[1], "_(") ||
			strings.Contains(tmp[1], "DWORD") ||
			strings.Contains(tmp[1], "EXTERN_C") ||
			strings.Contains(tmp[1], "__MINGW_NAME") ||
			(tmp[0]+"W" == tmp[1]) {
			continue
		}

		(*cache)[tmp[0]] = format(tmp[0])
		*lines = append(*lines, []string{format(tmp[0]), tmp[1]})
	}

	return nil
}

// DoesExist returns true if the specified path exists on disk, false
// otherwise.
func DoesExist(path string) bool {
	if _, err := os.Stat(ExpandPath(path)); err == nil {
		return true
	} else if os.IsNotExist(err) {
		return false
	} else {
		panic(err)
	}
}

// ExpandPath will expand the specified path accounting for ~ or ~user
// shortcuts as well as ENV vars.
func ExpandPath(path string) string {
	var e error
	var sep int
	var usr *user.User

	// Fix separators
	path = filepath.Clean(path)

	// Expand ENV vars
	path = os.ExpandEnv(path)

	// Expand ~
	if strings.HasPrefix(path, "~") {
		sep = strings.Index(path, string(filepath.Separator))

		switch sep {
		case -1:
			// If just ~
			if path == "~" {
				if usr, e = user.Current(); e != nil {
					return path
				}
			} else {
				// If ~user shortcut
				if usr, e = user.Lookup(path[1:]); e != nil {
					return path
				}
			}

			path = ""
		case 1:
			// If path starting with ~/
			if usr, e = user.Current(); e != nil {
				return path
			}

			path = path[2:]
		default:
			// If ~user/ shortcut
			if usr, e = user.Lookup(path[1:sep]); e != nil {
				return path
			}

			path = path[sep+1:]
		}

		return filepath.Join(usr.HomeDir, path)
	}

	// Otherwise just return path
	return path
}
