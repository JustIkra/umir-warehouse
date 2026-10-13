package profiles

type CatalogProvenance struct {
	Owner                 string `json:"owner"`
	Version               string `json:"version"`
	ArchiveSHA256         string `json:"archiveSHA256"`
	ArchiveBytes          int    `json:"archiveBytes"`
	EmbeddedArchiveSHA256 string `json:"embeddedArchiveSHA256"`
	BuildToolchain        string `json:"buildToolchain"`
	ToolchainImageID      string `json:"toolchainImageID"`
	ToolchainSourceSHA256 string `json:"toolchainSourceSHA256"`
	BinarySHA256          string `json:"binarySHA256"`
	SourceKind            string `json:"sourceKind"`
}
