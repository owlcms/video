package jobutil

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ssOwnerPattern = regexp.MustCompile(`"([^"]+)",pid=(\d+)`)

type PortProcess struct {
	PID     int
	Command string
}

type stopStep struct {
	action func(int) error
	wait   time.Duration
}

func FindUDPPortOwners(port int) ([]PortProcess, error) {
	if port <= 0 {
		return nil, nil
	}
	owners, err := findUDPPortOwners(port)
	if err != nil {
		return nil, err
	}
	return dedupePortProcesses(owners), nil
}

func StopProcessTree(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	if !processStillRunning(pid) {
		return nil
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for _, step := range processStopSteps() {
		if err := step.action(pid); err != nil {
			lastErr = err
		}
		if timeout <= 0 {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wait := step.wait
		if wait > remaining {
			wait = remaining
		}
		if wait > 0 {
			time.Sleep(wait)
		}
		if !processStillRunning(pid) {
			return nil
		}
	}
	if !processStillRunning(pid) {
		return nil
	}
	return lastErr
}

func StopUDPPortOwners(port int, timeout time.Duration, ignorePIDs ...int) error {
	if port <= 0 {
		return nil
	}
	ignored := ignorePIDSet(ignorePIDs)
	deadline := time.Now().Add(timeout)
	for _, step := range processStopSteps() {
		owners, err := FindUDPPortOwners(port)
		if err != nil {
			return err
		}
		owners = filterIgnoredOwners(owners, ignored)
		if len(owners) == 0 {
			return nil
		}
		for _, owner := range owners {
			_ = step.action(owner.PID)
		}
		if timeout <= 0 {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wait := step.wait
		if wait > remaining {
			wait = remaining
		}
		if err := WaitForUDPPortFree(port, wait, ignorePIDs...); err == nil {
			return nil
		}
	}
	return WaitForUDPPortFree(port, 0, ignorePIDs...)
}

func WaitForUDPPortFree(port int, timeout time.Duration, ignorePIDs ...int) error {
	if port <= 0 {
		return nil
	}
	ignored := ignorePIDSet(ignorePIDs)
	deadline := time.Now().Add(timeout)
	for {
		owners, err := FindUDPPortOwners(port)
		if err != nil {
			return err
		}
		owners = filterIgnoredOwners(owners, ignored)
		if len(owners) == 0 {
			return nil
		}
		if timeout <= 0 || time.Now().After(deadline) {
			return fmt.Errorf("port %d still in use by %s", port, DescribePortProcesses(owners))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func DescribePortProcesses(owners []PortProcess) string {
	if len(owners) == 0 {
		return "no processes"
	}
	parts := make([]string, 0, len(owners))
	for _, owner := range owners {
		name := strings.TrimSpace(owner.Command)
		if name == "" {
			parts = append(parts, fmt.Sprintf("pid %d", owner.PID))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", name, owner.PID))
	}
	return strings.Join(parts, ", ")
}

func processStopSteps() []stopStep {
	return []stopStep{
		{action: interruptProcess, wait: 250 * time.Millisecond},
		{action: terminateProcess, wait: 500 * time.Millisecond},
		{action: killProcess, wait: 750 * time.Millisecond},
	}
}

func dedupePortProcesses(owners []PortProcess) []PortProcess {
	if len(owners) == 0 {
		return nil
	}
	merged := make(map[int]PortProcess, len(owners))
	for _, owner := range owners {
		if owner.PID <= 0 {
			continue
		}
		existing, ok := merged[owner.PID]
		if !ok {
			merged[owner.PID] = owner
			continue
		}
		if strings.TrimSpace(existing.Command) == "" && strings.TrimSpace(owner.Command) != "" {
			existing.Command = owner.Command
			merged[owner.PID] = existing
		}
	}
	result := make([]PortProcess, 0, len(merged))
	for _, owner := range merged {
		result = append(result, owner)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Command != result[j].Command {
			return result[i].Command < result[j].Command
		}
		return result[i].PID < result[j].PID
	})
	return result
}

func filterIgnoredOwners(owners []PortProcess, ignored map[int]struct{}) []PortProcess {
	if len(owners) == 0 || len(ignored) == 0 {
		return owners
	}
	filtered := owners[:0]
	for _, owner := range owners {
		if _, skip := ignored[owner.PID]; skip {
			continue
		}
		filtered = append(filtered, owner)
	}
	return filtered
}

func ignorePIDSet(ignorePIDs []int) map[int]struct{} {
	if len(ignorePIDs) == 0 {
		return nil
	}
	ignored := make(map[int]struct{}, len(ignorePIDs))
	for _, pid := range ignorePIDs {
		if pid > 0 {
			ignored[pid] = struct{}{}
		}
	}
	return ignored
}

func parseSSUDPPortOwners(output []byte, port int) []PortProcess {
	target := ":" + strconv.Itoa(port)
	owners := make([]PortProcess, 0)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, target) {
			continue
		}
		for _, match := range ssOwnerPattern.FindAllStringSubmatch(line, -1) {
			pid, err := strconv.Atoi(match[2])
			if err != nil || pid <= 0 {
				continue
			}
			owners = append(owners, PortProcess{PID: pid, Command: strings.TrimSpace(match[1])})
		}
	}
	return owners
}

func parseLsofUDPPortOwners(output []byte, port int) []PortProcess {
	target := ":" + strconv.Itoa(port)
	owners := make([]PortProcess, 0)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "COMMAND") || !strings.Contains(line, target) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			continue
		}
		owners = append(owners, PortProcess{PID: pid, Command: strings.TrimSpace(fields[0])})
	}
	return owners
}

func parseWindowsNetstatUDPPortOwners(output []byte, port int) []PortProcess {
	owners := make([]PortProcess, 0)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || !strings.EqualFold(fields[0], "UDP") {
			continue
		}
		if !tokenHasPort(fields[1], port) {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid <= 0 {
			continue
		}
		owners = append(owners, PortProcess{PID: pid})
	}
	return owners
}

func parseWindowsTasklistName(output []byte) string {
	reader := csv.NewReader(bytes.NewReader(output))
	records, err := reader.ReadAll()
	if err != nil || len(records) == 0 || len(records[0]) == 0 {
		return ""
	}
	return strings.TrimSpace(records[0][0])
}

func tokenHasPort(token string, port int) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	idx := strings.LastIndex(token, ":")
	if idx < 0 || idx == len(token)-1 {
		return false
	}
	value, err := strconv.Atoi(strings.Trim(token[idx+1:], "[]"))
	if err != nil {
		return false
	}
	return value == port
}
