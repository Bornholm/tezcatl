package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestMeasureCollapsingOnPartitionList replays a real partition list
// ("<count> <environment>/<service>" per line, as `tezcatl templates`
// summarises it) through the collapsing rules. Point
// TEZCATL_PARTITIONS at one; it stays outside the repository, being a
// picture of somebody's machine.
func TestMeasureCollapsingOnPartitionList(t *testing.T) {
	path := os.Getenv("TEZCATL_PARTITIONS")
	if path == "" {
		t.Skip("set TEZCATL_PARTITIONS to a partition listing to run this")
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	before := map[string]int{}
	after := map[string]int{}
	templates := 0

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}

		count, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		environment, service, found := strings.Cut(fields[1], "/")
		if !found {
			continue
		}

		templates += count
		before[fields[1]] += count
		after[environment+"/"+collapseTransient(service)] += count
	}

	t.Logf("%d templates : %d partitions avant, %d après", templates, len(before), len(after))
}
