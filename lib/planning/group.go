package planning

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/usecase"
)

// bucket is a tentative grouping of usecases sharing journey+shape.
type bucket struct {
	JourneyID     string
	Shape         string
	Usecases      []usecase.UseCase
	Discriminator string // non-empty only when the journey has multiple buckets
}

// group partitions usecases by (journey_id, trigger.shape). Tag
// overlap further splits — usecases with disjoint tag sets in the
// same journey+shape go to different sensors. Evidence-directory
// proximity tightens further; usecases whose evidence files share a
// common directory (or 1-level-up) stay together.
func group(usecases []usecase.UseCase) []bucket {
	keyed := map[string][]usecase.UseCase{}
	var order []string
	for _, uc := range usecases {
		key := uc.JourneyID + "|" + uc.Trigger.Shape
		if _, ok := keyed[key]; !ok {
			order = append(order, key)
		}
		keyed[key] = append(keyed[key], uc)
	}
	sort.Strings(order)

	var out []bucket
	for _, k := range order {
		parts := strings.SplitN(k, "|", 2)
		clusters := splitByTagsAndEvidence(keyed[k])
		for _, c := range clusters {
			sort.Slice(c, func(i, j int) bool { return c[i].ID < c[j].ID })
			out = append(out, bucket{JourneyID: parts[0], Shape: parts[1], Usecases: c})
		}
	}
	return out
}

func splitByTagsAndEvidence(in []usecase.UseCase) [][]usecase.UseCase {
	if len(in) <= 1 {
		return [][]usecase.UseCase{in}
	}
	parent := make([]int, len(in))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	for i := 0; i < len(in); i++ {
		for j := i + 1; j < len(in); j++ {
			if shareTag(in[i], in[j]) || evidenceProximate(in[i], in[j]) {
				union(i, j)
			}
		}
	}
	groups := map[int][]usecase.UseCase{}
	for i, uc := range in {
		r := find(i)
		groups[r] = append(groups[r], uc)
	}
	var rootOrder []int
	for r := range groups {
		rootOrder = append(rootOrder, r)
	}
	sort.Ints(rootOrder)
	var out [][]usecase.UseCase
	for _, r := range rootOrder {
		out = append(out, groups[r])
	}
	return out
}

func shareTag(a, b usecase.UseCase) bool {
	set := map[string]struct{}{}
	for _, t := range a.Tags {
		set[t] = struct{}{}
	}
	for _, t := range b.Tags {
		if _, ok := set[t]; ok {
			return true
		}
	}
	return false
}

func evidenceProximate(a, b usecase.UseCase) bool {
	if len(a.Evidence) == 0 || len(b.Evidence) == 0 {
		return false
	}
	dirA := filepath.Dir(a.Evidence[0].File)
	dirB := filepath.Dir(b.Evidence[0].File)
	if dirA == dirB {
		return true
	}
	if filepath.Dir(dirA) == filepath.Dir(dirB) && dirA != "." && dirB != "." {
		return true
	}
	return false
}

// assignDiscriminators populates bucket.Discriminator for every
// bucket that belongs to a journey with more than one bucket. The
// discriminator is deterministic and computed in priority order:
//
//  1. Dominant tag — a tag present in EVERY usecase of the bucket. If
//     multiple such tags exist, the alphabetically first is chosen.
//  2. Slugified trigger shape (e.g. "cli-invocation", "http-request").
//  3. Last-resort stable "cluster-N" index reflecting emergence order
//     from group (1-based, scoped to the journey).
//
// Single-bucket journeys leave discriminator empty so the simple
// "<prefix>-<journey>" sensor_id shape is preserved.
func assignDiscriminators(buckets []bucket) {
	counts := map[string]int{}
	for _, b := range buckets {
		counts[b.JourneyID]++
	}
	seenInJourney := map[string]int{}
	for i := range buckets {
		j := buckets[i].JourneyID
		if counts[j] <= 1 {
			continue
		}
		seenInJourney[j]++
		buckets[i].Discriminator = computeDiscriminator(buckets[i], seenInJourney[j])
	}
}

func computeDiscriminator(b bucket, clusterIdx int) string {
	if tag := dominantTag(b.Usecases); tag != "" {
		return Slugify(tag)
	}
	if shape := Slugify(b.Shape); shape != "" {
		return shape
	}
	return fmt.Sprintf("cluster-%d", clusterIdx)
}

// dominantTag returns the alphabetically-first tag shared by EVERY
// usecase in the bucket, or "" when no such tag exists. A usecase
// with no tags forces the empty result.
func dominantTag(ucs []usecase.UseCase) string {
	if len(ucs) == 0 {
		return ""
	}
	intersect := map[string]struct{}{}
	for _, t := range ucs[0].Tags {
		intersect[t] = struct{}{}
	}
	if len(intersect) == 0 {
		return ""
	}
	for _, uc := range ucs[1:] {
		present := map[string]struct{}{}
		for _, t := range uc.Tags {
			if _, ok := intersect[t]; ok {
				present[t] = struct{}{}
			}
		}
		intersect = present
		if len(intersect) == 0 {
			return ""
		}
	}
	var shared []string
	for t := range intersect {
		shared = append(shared, t)
	}
	sort.Strings(shared)
	return shared[0]
}
