package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alecthomas/kingpin"
	nested "github.com/antonfisher/nested-logrus-formatter"
	"github.com/sbreitf1/exec"
	"github.com/sbreitf1/fs"
	log "github.com/sirupsen/logrus"
	elf "github.com/yalue/elf_reader"
)

const (
	modeLDD     = "LDD"
	modeReadELF = "ReadELF"
	modeParse   = "Parse"

	findModeLD     = "LD"
	findModeSearch = "Search"

	machineTypeUnknown = ""
	machineTypeX86     = "x86"
	machineTypeAMD64   = "x86-64"
)

var (
	appMain      = kingpin.New("gather-dependencies", "Copy all shared dependencies into a single directory")
	argInputFile = appMain.Arg("in", "Input binary file").Required().String()
	argOutputDir = appMain.Arg("out", "Output directory").Required().String()

	mode        = appMain.Flag("mode", "Mode can be one of '"+modeLDD+"', '"+modeReadELF+"' or '"+modeParse+"'.").Short('m').Default(modeReadELF).String()
	findMode    = appMain.Flag("find", "Find mode can be one of '"+findModeLD+"' or '"+findModeSearch+"'.").Short('f').Default(findModeLD).String()
	machineType = machineTypeUnknown
)

func main() {
	log.SetFormatter(&nested.Formatter{
		HideKeys:        true,
		TimestampFormat: "15:04:05",
		NoColors:        true,
		TrimMessages:    true,
	})

	kingpin.MustParse(appMain.Parse(os.Args[1:]))

	inputFile, err := filepath.Abs(*argInputFile)
	if err != nil {
		log.Fatalf("Invalid input file: %v", err)
	}
	outputDir, err := filepath.Abs(*argOutputDir)
	if err != nil {
		log.Fatalf("Invalid output dir: %v", err)
	}

	log.Infof("Input file %q", inputFile)
	machineType, err = getLibraryMachineType(inputFile)
	if err != nil {
		log.Fatalf("Unable to detect binary machine type: %v", err)
	}
	if machineType == machineTypeUnknown {
		log.Fatalf("Unknown binary machine type")
	}
	log.Infof("-> Machine Type %s", machineType)
	log.Infof("Output dir %q", *argOutputDir)

	files, err := getAllDependencies(inputFile)
	if err != nil {
		log.Fatalf("Could not retrieve dependencies: %v", err)
	}

	for _, file := range files {
		log.Infof("Gather file %q", file)

		dstFile := filepath.Join(outputDir, file)
		dstDir := filepath.Dir(dstFile)
		if err := os.MkdirAll(dstDir, os.ModePerm); err != nil {
			log.Fatalf("-> Could not create parent directory: %v", err)
		}

		if err := fs.CopyFile(file, dstFile); err != nil {
			log.Fatalf("-> Failed to copy file: %v", err)
		}
	}

	log.Infof("%d dependencies have been gathered", len(files))
}

func getAllDependencies(file string) ([]string, error) {
	switch *mode {
	case modeLDD:
		return getDependenciesRecursiveLDD(file)

	case modeReadELF:
		fallthrough
	case modeParse:
		return getDependenciesRecursive(file)

	default:
		return nil, fmt.Errorf("Invalid mode %q", *mode)
	}
}

func getDependenciesRecursiveLDD(file string) ([]string, error) {
	result, code, err := exec.Run("ldd", file)
	if err != nil {
		return nil, fmt.Errorf("Could not execute 'ldd': %v", err)
	}
	if code != 0 {
		log.Info(result)
		return nil, fmt.Errorf("Code %d returned by 'ldd'", code)
	}

	pattern := regexp.MustCompile(`\s([a-zA-Z0-9.\-_/]+)(\s+=>\s+([a-zA-Z0-9.\-_/]+))?\s+\(0x[0-9a-f]+\)`)
	matches := pattern.FindAllStringSubmatch(result, -1)

	files := make([]string, 0)
	for _, m := range matches {
		var depFile string
		if len(m[3]) == 0 {
			depFile = m[1]
		} else {
			depFile = m[3]
		}

		if strings.HasPrefix(depFile, "/") {
			files = append(files, depFile)
		}
	}

	return files, nil
}

func getDependenciesRecursive(file string) ([]string, error) {
	open, err := getDependencies(file)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, lib := range open {
		seen[lib] = true
	}

	files := make([]string, 0)
	for len(open) > 0 {
		lib := open[0]
		open = open[1:]

		libs, err := getDependencies(lib)
		if err != nil {
			return nil, err
		}
		files = append(files, lib)

		for _, lib := range libs {
			if _, ok := seen[lib]; !ok {
				open = append(open, lib)
				seen[lib] = true
			}
		}
	}

	return files, nil
}

func getDependencies(file string) ([]string, error) {
	switch *mode {
	case modeReadELF:
		return getDependenciesReadELF(file)

	default:
		return nil, fmt.Errorf("Invalid mode %q", *mode)
	}
}

func getDependenciesReadELF(file string) ([]string, error) {
	result, code, err := exec.Run("readelf", "-d", file)
	if err != nil {
		return nil, fmt.Errorf("Could not execute 'ldd': %v", err)
	}
	if code != 0 {
		log.Info(result)
		return nil, fmt.Errorf("Code %d returned by 'ldd'", code)
	}

	pattern := regexp.MustCompile(`0x\d+\s+\(NEEDED\)[^[]+\[([a-zA-Z0-9.\-_]+)\]`)
	matches := pattern.FindAllStringSubmatch(result, -1)

	files := make([]string, 0)
	for _, m := range matches {
		file, err := findLibrary(m[1])
		if err != nil {
			return nil, fmt.Errorf("Could not find library file %q: %v", m[1], err)
		}
		files = append(files, file)
	}

	return files, nil
}

func doelf(file string) {
	raw, err := ioutil.ReadFile(file)
	if err != nil {
		panic(err)
	}
	elfFile, err := elf.ParseELFFile(raw)
	if err != nil {
		panic(err)
	}
	count := elfFile.GetSectionCount()
	for i := uint16(0); i < count; i++ {
		/*name, err := elfFile.GetSectionName(i)
		if err != nil {
			panic(err)
		}*/

		header, err := elfFile.GetSectionHeader(i)
		if err != nil {
			panic(err)
		}

		if header.GetType() == elf.DynamicLinkingTableSection {
			entries, err := elfFile.DynamicEntries(i)
			if err != nil {
				panic(err)
			}
			for _, entry := range entries {
				if entry.GetTag().GetValue() == 1 {
					fmt.Println(entry.GetTag(), "->", entry.GetTag().GetValue(), "->", entry.GetValue())
				}
			}
		}

		if elfFile.IsStringTable(i) {
			entries, _ := elfFile.GetStringTable(i)
			count := 0
			for j, entry := range entries {
				if is(entry) {
					name, _ := elfFile.GetSectionName(i)
					fmt.Println(name, "->", j, "->", entry, "=>", count)
				}
				count += len(entry) + 1
			}
		}
	}
}

func is(str string) bool {
	return strings.Contains(str, "libxml2.so.2") || strings.Contains(str, "libpthread.so.0") || strings.Contains(str, "libc.so.6")
}

func findLibrary(name string) (string, error) {
	switch *findMode {
	case findModeLD:
		return findLibraryLDConfig(name)

	default:
		return "", fmt.Errorf("Invalid find mode %q", *findMode)
	}
}

func findLibraryLDConfig(name string) (string, error) {
	result, code, err := exec.Run("ldconfig", "-p")
	if err != nil {
		return "", fmt.Errorf("Could not execute 'ldconfig': %v", err)
	}
	if code != 0 {
		log.Info(result)
		return "", fmt.Errorf("Code %d returned by 'ldconfig'", code)
	}

	pattern := regexp.MustCompile(`([([a-zA-Z0-9.\-_]+)\s+\([^)]+\)\s+=>\s+([([a-zA-Z0-9.\-_/]+)`)
	matches := pattern.FindAllStringSubmatch(result, -1)

	candidates := make([]string, 0)
	for _, m := range matches {
		if m[1] == name {
			candidates = append(candidates, m[2])
		}
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("Library %q not found in library cache", name)
	}

	return selectLibrary(name, candidates)
}

func selectLibrary(name string, candidates []string) (string, error) {
	for _, lib := range candidates {
		libType, err := getLibraryMachineType(lib)
		if err != nil {
			return "", err
		}
		if libType == machineType {
			return lib, nil
		}
	}
	return "", fmt.Errorf("Library %q not available for machine type %q", name, machineType)
}

func getLibraryMachineType(path string) (string, error) {
	raw, err := ioutil.ReadFile(path)
	if err != nil {
		panic(err)
	}
	elfFile, err := elf.ParseELFFile(raw)
	if err != nil {
		panic(err)
	}

	switch elfFile.(type) {
	case *elf.ELF32File:
		/*switch e.Header.Machine {
		case elf.MachineTypeX86:
			return machineTypeX86, nil
		default:
			log.Warnf("Unknown machine type %q of %q", e.Header.Machine, path)
			return machineTypeUnknown, nil
		}*/
		return machineTypeX86, nil

	case *elf.ELF64File:
		/*switch e.Header.Machine {
		case elf.MachineTypeAMD64:
			return machineTypeAMD64, nil
		default:
			log.Warnf("Unknown machine type %q of %q", e.Header.Machine, path)
			return machineTypeUnknown, nil
		}*/
		return machineTypeAMD64, nil

	default:
		// ignore unknown architectures
		log.Warnf("Unknown ELF Type %T", elfFile)
		return machineTypeUnknown, nil
	}
}
