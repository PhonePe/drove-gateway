package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/renameio"
	"github.com/sirupsen/logrus"
)

// Markers used to delimit drove-gateway managed server blocks inside the HAProxy config.
// HAProxy treats these lines as comments, so the config stays valid even before a block is
// populated. The begin marker carries the target backend name, for example:
//
//	backend be_static
//	    #DROVE-SERVERS-BEGIN be_static
//	    #DROVE-SERVERS-END
//
// drove-gateway rewrites the lines between the markers (using the HAProxy server state file) before
// HAProxy is started/reloaded so that "load-server-state-from-file" has matching server objects to
// restore state for servers that were previously added dynamically via the runtime API.
const (
	serverStateBlockBeginPrefix = "#DROVE-SERVERS-BEGIN"
	serverStateBlockEndMarker   = "#DROVE-SERVERS-END"
	haproxyServerStateSchemaV1  = 1
)

type serverStateEntry struct {
	name string
	addr string
	port string
}

// syncHaproxyServerStateConfigBlocks updates the drove-managed server blocks in the HAProxy config
// from the current HAProxy server state file. It is intended to be invoked from systemd
// ExecStartPre / ExecReload (via the -sync-haproxy-state-config flag) before HAProxy (re)starts,
// so that the servers referenced by the server state file already exist in the loaded config.
func syncHaproxyServerStateConfigBlocks() error {
	if config.ProxyPlatform != "haproxy" {
		return errors.New("-sync-haproxy-state-config is only valid when proxy_platform is haproxy")
	}
	if !config.HaproxyManageGlobalServerStateFile {
		return errors.New("-sync-haproxy-state-config requires haproxy_manage_global_server_state_file = true")
	}
	configPath := config.HaproxyConfig
	if configPath == "" {
		return errors.New("haproxy_config path is not configured")
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read haproxy_config %q: %w", configPath, err)
	}
	contentStr, err := validatedConfigText(content)
	if err != nil {
		return fmt.Errorf("invalid haproxy_config %q content: %w", configPath, err)
	}

	serversByBackend, stateErr := parseServerStateFileByBackend(config.HaproxyGlobalServerStateFilePath)
	if stateErr != nil {
		// On the very first (re)start there may be no server state file yet. In that case we keep the
		// markers but empty their contents so HAProxy starts cleanly and the runtime API repopulates
		// dynamic servers afterwards.
		if errors.Is(stateErr, os.ErrNotExist) {
			logger.WithField("path", config.HaproxyGlobalServerStateFilePath).Warn("HAProxy server state file not found; drove-managed blocks will be emptied")
			fmt.Fprintf(os.Stderr, "nixy[haproxy-state-sync]: server state file not found at %s; managed blocks will be emptied\n", config.HaproxyGlobalServerStateFilePath)
			serversByBackend = map[string][]serverStateEntry{}
		} else {
			return fmt.Errorf("failed to parse HAProxy server state file: %w", stateErr)
		}
	}

	backendsInStateFile := len(serversByBackend)
	serversInStateFile := 0
	for _, entries := range serversByBackend {
		serversInStateFile += len(entries)
	}

	newContent, blockCount, err := rewriteServerStateConfigBlocks(contentStr, serversByBackend)
	if err != nil {
		return err
	}
	if blockCount == 0 {
		return fmt.Errorf("haproxy_manage_global_server_state_file is enabled but no %s / %s blocks were found in haproxy_config %q", serverStateBlockBeginPrefix, serverStateBlockEndMarker, configPath)
	}

	if newContent == contentStr {
		logger.WithField("path", configPath).Info("Drove-managed HAProxy server state blocks are already up to date")
		fmt.Fprintf(os.Stdout, "nixy[haproxy-state-sync]: no changes (config=%s blocks=%d backends_in_state_file=%d servers_in_state_file=%d)\n", configPath, blockCount, backendsInStateFile, serversInStateFile)
		return nil
	}

	if err := writeHaproxyConfigAtomic(configPath, []byte(newContent)); err != nil {
		return fmt.Errorf("failed to write updated haproxy_config %q: %w", configPath, err)
	}

	logger.WithFields(logrus.Fields{
		"path":   configPath,
		"blocks": blockCount,
	}).Info("Updated drove-managed HAProxy server state blocks")
	fmt.Fprintf(os.Stdout, "nixy[haproxy-state-sync]: updated managed blocks (config=%s blocks=%d backends_in_state_file=%d servers_in_state_file=%d)\n", configPath, blockCount, backendsInStateFile, serversInStateFile)
	return nil
}

// rewriteServerStateConfigBlocks replaces the content of each drove-managed block with the desired
// server lines for that block's backend. It returns the rewritten config and the number of blocks
// found. Lines outside the markers are preserved verbatim.
func rewriteServerStateConfigBlocks(content string, serversByBackend map[string][]serverStateEntry) (string, int, error) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	blockCount := 0

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if !strings.HasPrefix(trimmed, serverStateBlockBeginPrefix) {
			out = append(out, line)
			continue
		}

		backend := strings.TrimSpace(strings.TrimPrefix(trimmed, serverStateBlockBeginPrefix))
		if backend == "" {
			return "", 0, fmt.Errorf("%s marker is missing a backend name", serverStateBlockBeginPrefix)
		}

		// Preserve the indentation of the begin marker for the emitted server lines.
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]

		// Keep the begin marker line unchanged.
		out = append(out, line)

		// Emit the managed server lines for this backend (may be none).
		for _, srv := range serversByBackend[backend] {
			out = append(out, fmt.Sprintf("%sserver %s %s", indent, srv.name, formatServerEndpoint(srv.addr, srv.port)))
		}

		// Skip the previous block contents until the matching end marker.
		end := -1
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == serverStateBlockEndMarker {
				end = j
				break
			}
		}
		if end == -1 {
			return "", 0, fmt.Errorf("%s for backend %q has no matching %s", serverStateBlockBeginPrefix, backend, serverStateBlockEndMarker)
		}

		out = append(out, lines[end]) // preserve the end marker line
		blockCount++
		i = end
	}

	return strings.Join(out, "\n"), blockCount, nil
}

// formatServerEndpoint returns a host:port endpoint suitable for HAProxy server lines.
// IPv6 literals must be bracketed before appending the port.
func formatServerEndpoint(addr, port string) string {
	host := strings.TrimSpace(addr)
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = "[" + host + "]"
	} else if strings.Contains(host, ":") && !(strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")) {
		host = "[" + host + "]"
	}
	return host + ":" + port
}

// parseServerStateFileByBackend parses a HAProxy "show servers state" formatted file and returns
// the servers grouped by backend name.
func parseServerStateFileByBackend(path string) (map[string][]serverStateEntry, error) {
	if path == "" {
		return nil, errors.New("haproxy_global_server_state_file_path is not configured")
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	result := make(map[string][]serverStateEntry)

	// Default column indexes for server state file format version 1.
	beNameIdx, srvNameIdx, srvAddrIdx, srvPortIdx := 1, 3, 4, 18
	headerParsed := false
	versionValidated := false

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			// Ignore blank lines.
			continue
		}

		if !versionValidated {
			if err := validateHaproxyServerStateSchemaVersion(trimmedLine); err != nil {
				return nil, fmt.Errorf("invalid HAProxy server state file schema version: %w", err)
			}
			versionValidated = true
			continue
		}

		if strings.HasPrefix(trimmedLine, "#") {
			// The header comment lists the column names; use it to be resilient to format changes.
			if !headerParsed {
				cols := strings.Fields(strings.TrimPrefix(trimmedLine, "#"))
				idx := make(map[string]int, len(cols))
				for n, c := range cols {
					idx[c] = n
				}
				if v, ok := idx["be_name"]; ok {
					beNameIdx = v
				}
				if v, ok := idx["srv_name"]; ok {
					srvNameIdx = v
				}
				if v, ok := idx["srv_addr"]; ok {
					srvAddrIdx = v
				}
				if v, ok := idx["srv_port"]; ok {
					srvPortIdx = v
				}
				headerParsed = true
			}
			continue
		}

		fields := strings.Fields(line)
		maxIdx := max(beNameIdx, srvNameIdx, srvAddrIdx, srvPortIdx)
		if len(fields) <= maxIdx {
			continue
		}

		addr := fields[srvAddrIdx]
		if addr == "" || addr == "-" {
			// Skip servers without a usable address (e.g. placeholder/template slots).
			continue
		}

		port := strings.TrimSpace(fields[srvPortIdx])
		portNumber, portErr := strconv.Atoi(port)
		if portErr != nil || portNumber < 1 || portNumber > 65535 {
			// Skip servers without a usable port.
			continue
		}

		backend := fields[beNameIdx]
		result[backend] = append(result[backend], serverStateEntry{
			name: fields[srvNameIdx],
			addr: addr,
			port: port,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !versionValidated {
		return nil, errors.New("missing HAProxy server state schema version line")
	}
	return result, nil
}

func validateHaproxyServerStateSchemaVersion(raw string) error {
	version, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("expected integer schema version, got %q", raw)
	}
	if version != haproxyServerStateSchemaV1 {
		return fmt.Errorf("unsupported schema version %d (supported: %d)", version, haproxyServerStateSchemaV1)
	}
	return nil
}

// haproxyServerStateBlocksPresent reports whether the HAProxy config contains at least one
// drove-managed server block. Used to enforce that the blocks are present when server state file
// management is enabled with reloads disabled.
func haproxyServerStateBlocksPresent(configPath string) (bool, error) {
	if configPath == "" {
		return false, errors.New("haproxy_config path is not configured")
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), serverStateBlockBeginPrefix) {
			return true, nil
		}
	}
	return false, nil
}

// writeHaproxyConfigAtomic writes the HAProxy config via a temp file + rename in the same directory
// so HAProxy never reads a partially written config.
func writeHaproxyConfigAtomic(configPath string, content []byte) error {
	currentInfo, err := os.Stat(configPath)
	if err != nil {
		return fmt.Errorf("failed to stat existing haproxy config at %q: %w", configPath, err)
	}

	if err := renameio.WriteFile(configPath, content, currentInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("failed to atomically write haproxy config at %q: %w", configPath, err)
	}
	return nil
}

func validatedConfigText(content []byte) (string, error) {
	if !utf8.Valid(content) {
		return "", errors.New("not valid UTF-8")
	}
	if bytes.IndexByte(content, 0) != -1 {
		return "", errors.New("contains NUL bytes")
	}
	return string(content), nil
}
