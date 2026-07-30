package transient

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// platformBudget is ~60% of MemAvailable: the containerized engine shares its VM's memory with
// the other stack services, so total physical memory would overshoot what is actually free.
func platformBudget() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return (kb << 10) * 6 / 10
		}
	}
	return 0
}
