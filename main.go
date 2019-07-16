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

	patternLibName = "[a-zA-Z0-9.\\-_+]+"
	patternLibPath = "[a-zA-Z0-9.\\-_+/]+"
)

var (
	appMain      = kingpin.New("gather-dependencies", "Copy all shared dependencies into a single directory")
	argInputFile = appMain.Arg("in", "Input binary file").Required().String()
	argOutputDir = appMain.Arg("out", "Output directory").Required().String()

	clean       = appMain.Flag("clean", "Delete all files from output directory").Bool()
	mode        = appMain.Flag("mode", "Mode can be one of '"+modeLDD+"', '"+modeReadELF+"' or '"+modeParse+"'.").Short('m').Default(modeReadELF).String()
	findMode    = appMain.Flag("findmode", "Find mode can be one of '"+findModeLD+"' or '"+findModeSearch+"'.").Short('f').Default(findModeLD).String()
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
	log.Infof("Output dir %q", outputDir)

	if *clean {
		log.Infof("Clean output directory")
		files, err := ioutil.ReadDir(outputDir)
		if err != nil {
			log.Fatalf("Failed to clean output directory")
		}
		for _, file := range files {
			path := filepath.Join(outputDir, file.Name())
			if file.IsDir() {
				if err := os.RemoveAll(path); err != nil {
					log.Fatalf("Failed to delete directory %q", path)
				}
			} else {
				if err := os.Remove(filepath.Join(outputDir, file.Name())); err != nil {
					log.Fatalf("Failed to delete file %q", path)
				}
			}
		}
	}

	files, err := getAllDependencies(inputFile)
	if err != nil {
		log.Fatalf("Could not retrieve dependencies: %v", err)
	}

	log.Infof("Copy dependencies to output dir")
	for _, file := range files {
		log.Infof("-> Gather file %q", file)

		dstFile := filepath.Join(outputDir, file)
		dstDir := filepath.Dir(dstFile)
		if err := os.MkdirAll(dstDir, os.ModePerm); err != nil {
			log.Fatalf("Could not create parent directory: %v", err)
		}

		if err := fs.CopyFile(file, dstFile); err != nil {
			log.Fatalf("Failed to copy file: %v", err)
		}
	}

	log.Infof("%d dependencies have been gathered", len(files))
}

/* ############################################## */
/* ###           Dependency Listing           ### */
/* ############################################## */

func getAllDependencies(file string) ([]string, error) {
	log.Infof("Retrieve dependencies")

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

	pattern := regexp.MustCompile(`\s(` + patternLibName + `)(\s+=>\s+(` + patternLibPath + `))?\s+\(0x[0-9a-f]+\)`)
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

/* ############################################## */
/* ###      Recursive Dependency Listing      ### */
/* ############################################## */

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

		log.Infof("-> Process %q", lib)

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

	case modeParse:
		return getDependenciesParse(file)

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

	pattern := regexp.MustCompile(`0x\d+\s+\(NEEDED\)[^[]+\[(` + patternLibName + `)\]`)
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

func getDependenciesParse(file string) ([]string, error) {
	raw, err := ioutil.ReadFile(file)
	if err != nil {
		panic(err)
	}
	elfFile, err := elf.ParseELFFile(raw)
	if err != nil {
		panic(err)
	}

	count := elfFile.GetSectionCount()

	// find dynamic section
	dynSection := uint16(0)
	for i := uint16(1); i < count; i++ {
		name, err := elfFile.GetSectionName(i)
		if err != nil {
			return nil, fmt.Errorf("Failed to parse %q: %v", file, err)
		}
		if name == ".dynamic" {
			dynSection = i
			break
		}
	}

	if dynSection == uint16(0) {
		return nil, fmt.Errorf("No dynamic section in %q found. Probably statically linked", file)
	}

	// find dynamic string section
	dynStrSection := uint16(0)
	for i := uint16(1); i < count; i++ {
		name, err := elfFile.GetSectionName(i)
		if err != nil {
			return nil, fmt.Errorf("Failed to parse %q: %v", file, err)
		}
		if name == ".dynstr" {
			dynStrSection = i
			break
		}
	}

	if dynStrSection == uint16(0) {
		return nil, fmt.Errorf("No dynamic strings section in %q found. Probably statically linked", file)
	}

	entries, err := elfFile.DynamicEntries(dynSection)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse %q: %v", file, err)
	}
	data, err := elfFile.GetSectionContent(dynStrSection)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse %q: %v", file, err)
	}

	files := make([]string, 0)
	for _, entry := range entries {
		// NEEDED flag (1)
		if entry.GetTag().GetValue() == 1 {
			start := entry.GetValue()
			end := start
			for ; end < uint64(len(data)); end++ {
				if data[end] == 0 {
					break
				}
			}
			str := string(data[start:end])
			path, err := findLibrary(str)
			if err != nil {
				return nil, fmt.Errorf("Could not find library file %q: %v", str, err)
			}
			files = append(files, path)
		}
	}

	return files, nil
}

/* ############################################## */
/* ###             Find Libraries             ### */
/* ############################################## */

func findLibrary(name string) (string, error) {
	switch *findMode {
	case findModeLD:
		return findLibraryLDConfig(name)

	case findModeSearch:
		return "", fmt.Errorf("Find mode %q is not implemented yet", findModeSearch)

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

	pattern := regexp.MustCompile(`([(` + patternLibName + `)\s+\([^)]+\)\s+=>\s+(` + patternLibPath + `)`)
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
