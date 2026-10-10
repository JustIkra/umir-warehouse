package testsupport

func IDSequence(values ...string) func() string {
	ids := append([]string(nil), values...)
	index := 0
	return func() string {
		if index >= len(ids) {
			return ""
		}
		id := ids[index]
		index++
		return id
	}
}
