package readonly

import (
	"ragflow/internal/readonly/resultdigest"
	"ragflow/internal/readonly/starrocksguard"
)

// StarRocksPolicy binds actual metadata and versioned physical mappings. Scope
// filters originate only in the protected native-user policy, never model JSON.
type StarRocksPolicy struct {
	KnowledgeChunks   map[string]string         `json:"knowledge_chunks"`
	AsOf              string                    `json:"as_of,omitempty"`
	CatalogSHA256     string                    `json:"catalog_sha256"`
	MappingSHA256     string                    `json:"mapping_sha256"`
	DefinitionSHA256  string                    `json:"definition_sha256"`
	CurrentUserSHA256 string                    `json:"current_user_sha256"`
	Relations         []starrocksguard.Relation `json:"relations"`
	Functions         []string                  `json:"functions"`
}
type ResultSummary = resultdigest.Summary
