package pack

import "strings"

func CompareVersions(a, b string) int {
	na, prea := splitVersion(a)
	nb, preb := splitVersion(b)
	for i := 0; i < 3; i++ {
		if na[i] != nb[i] {
			if na[i] < nb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case prea == "" && preb != "":
		return 1
	case prea != "" && preb == "":
		return -1
	case prea == preb:
		return 0
	case prea < preb:
		return -1
	default:
		return 1
	}
}

func splitVersion(v string) ([3]int, string) {
	var nums [3]int
	pre := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n := 0
		for _, c := range part {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		nums[i] = n
	}
	return nums, pre
}
