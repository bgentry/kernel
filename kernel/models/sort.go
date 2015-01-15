package models

func (s Apps) Len() int {
	return len(s)
}

func (s Apps) Less(i, j int) bool {
	return s[i].Name < s[j].Name
}

func (s Apps) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s Clusters) Len() int {
	return len(s)
}

func (s Clusters) Less(i, j int) bool {
	return s[i].Name < s[j].Name
}

func (s Clusters) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
