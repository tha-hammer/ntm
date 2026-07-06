package pipeline

import (
	"errors"
	"fmt"
	"strings"
)

var errNoAdjudicatorPane = errors.New("no available adjudicator pane")
var errNoModelFamilyPane = errors.New("no pane matches model family")
var errNoPaneForStrategy = errors.New("no pane available for assignment strategy")
var errMalformedPaneDomainRoster = errors.New("malformed pane domain roster")

type paneStrategyPane struct {
	ID          string
	ModelFamily string
	Domains     []string
	Excluded    bool
}

func (p paneStrategyPane) available() bool {
	return p.ID != "" && !p.Excluded
}

// rotateAdjudicator chooses an adjudicator pane from orderedPanes while
// excluding the current debate champions. Among eligible panes, it picks the
// pane with the longest gap since it last adjudicated. Ties keep orderedPanes
// order, which makes the strategy deterministic.
func rotateAdjudicator(orderedPanes []string, champions []string, adjudicatorHistory []string) (string, error) {
	championSet := make(map[string]struct{}, len(champions))
	for _, paneID := range champions {
		if paneID != "" {
			championSet[paneID] = struct{}{}
		}
	}

	lastSeen := make(map[string]int, len(adjudicatorHistory))
	for idx, paneID := range adjudicatorHistory {
		if paneID != "" {
			lastSeen[paneID] = idx
		}
	}

	bestPane := ""
	bestGap := -1
	for _, paneID := range orderedPanes {
		if paneID == "" {
			continue
		}
		if _, champion := championSet[paneID]; champion {
			continue
		}

		gap := len(adjudicatorHistory) + 1
		if idx, ok := lastSeen[paneID]; ok {
			gap = len(adjudicatorHistory) - idx
		}
		if gap > bestGap {
			bestPane = paneID
			bestGap = gap
		}
	}
	if bestPane == "" {
		return "", errNoAdjudicatorPane
	}
	return bestPane, nil
}

// byModelFamily chooses the first pane whose model family matches the current
// foreach item. The ordered input preserves deterministic routing when several
// panes share a family.
func byModelFamily(orderedPanes []paneStrategyPane, modelFamily string) (string, error) {
	if modelFamily == "" {
		return "", errNoModelFamilyPane
	}
	for _, pane := range orderedPanes {
		if pane.available() && pane.ModelFamily == modelFamily {
			return pane.ID, nil
		}
	}
	return "", errNoModelFamilyPane
}

// byModelFamilyDifference chooses the first pane whose model family differs
// from the item's authoring family. If every pane has the same family, it
// falls back to the first available pane and reports warnFallback=true.
//
// An empty authorModelFamily is rejected: without a baseline the strategy
// cannot enforce the cross-family adversarial contract and would otherwise
// silently route to the first non-empty-family pane. Mirrors byModelFamily's
// missing-family behavior.
func byModelFamilyDifference(orderedPanes []paneStrategyPane, authorModelFamily string) (paneID string, warnFallback bool, err error) {
	if authorModelFamily == "" {
		return "", false, errNoModelFamilyPane
	}
	firstPane := ""
	for _, pane := range orderedPanes {
		if !pane.available() {
			continue
		}
		if firstPane == "" {
			firstPane = pane.ID
		}
		if pane.ModelFamily != authorModelFamily {
			return pane.ID, false, nil
		}
	}
	if firstPane == "" {
		return "", false, errNoPaneForStrategy
	}
	return firstPane, true, nil
}

// roundRobinByDomain chooses the first pane whose domain list contains the
// current item domain. If no pane owns the domain, it falls back to normal
// round-robin assignment using iterationIndex.
func roundRobinByDomain(orderedPanes []paneStrategyPane, domain string, iterationIndex int) (string, error) {
	for _, pane := range orderedPanes {
		if !pane.available() {
			continue
		}
		for _, paneDomain := range pane.Domains {
			if paneDomain == domain {
				return pane.ID, nil
			}
		}
	}
	return roundRobinPane(orderedPanes, iterationIndex)
}

func roundRobinPane(orderedPanes []paneStrategyPane, iterationIndex int) (string, error) {
	var paneIDs []string
	for _, pane := range orderedPanes {
		if pane.available() {
			paneIDs = append(paneIDs, pane.ID)
		}
	}
	if len(paneIDs) == 0 {
		return "", errNoPaneForStrategy
	}
	if iterationIndex < 0 {
		iterationIndex = 0
	}
	return paneIDs[iterationIndex%len(paneIDs)], nil
}

// parsePaneDomainRoster parses a compact roster section with entries like:
//
//	pane p2:
//	  domain: [H-001, H-005]
func parsePaneDomainRoster(content string) ([]paneStrategyPane, error) {
	var panes []paneStrategyPane
	currentPane := -1
	for lineNo, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "pane ") && strings.HasSuffix(trimmed, ":") {
			paneID := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "pane "), ":"))
			if paneID == "" {
				return nil, fmt.Errorf("%w: line %d has empty pane id", errMalformedPaneDomainRoster, lineNo+1)
			}
			panes = append(panes, paneStrategyPane{ID: paneID})
			currentPane = len(panes) - 1
			continue
		}
		if strings.HasPrefix(trimmed, "domain:") {
			if currentPane < 0 {
				return nil, fmt.Errorf("%w: line %d domain appears before pane", errMalformedPaneDomainRoster, lineNo+1)
			}
			domains, err := parseRosterDomainList(strings.TrimSpace(strings.TrimPrefix(trimmed, "domain:")))
			if err != nil {
				return nil, fmt.Errorf("%w: line %d: %v", errMalformedPaneDomainRoster, lineNo+1, err)
			}
			panes[currentPane].Domains = domains
			continue
		}
		return nil, fmt.Errorf("%w: line %d unrecognized roster line %q", errMalformedPaneDomainRoster, lineNo+1, trimmed)
	}
	return panes, nil
}

func parseRosterDomainList(raw string) ([]string, error) {
	if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
		return nil, errors.New("domain list must use [A, B] form")
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]"))
	if inner == "" {
		return nil, nil
	}
	parts := strings.Split(inner, ",")
	domains := make([]string, 0, len(parts))
	for _, part := range parts {
		domain := strings.Trim(strings.TrimSpace(part), `"'`)
		if domain == "" {
			return nil, errors.New("domain list contains an empty entry")
		}
		domains = append(domains, domain)
	}
	return domains, nil
}
