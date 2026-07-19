package catalog

const (
	uuidPattern   = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
	hash64Pattern = `^[0-9a-f]{64}$`
	gitOIDPattern = `^(?:[0-9a-f]{40}|[0-9a-f]{64})$`
)

const searchKnowledgeInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["query","mode","limit","source_ids","source_version_ids"],
  "properties":{
    "query":{"type":"string","minLength":1,"maxLength":8192},
    "mode":{"type":"string","enum":["keyword","semantic","hybrid"]},
    "limit":{"type":"integer","minimum":1,"maximum":100},
    "source_ids":{"type":"array","maxItems":100,"uniqueItems":true,"items":{"type":"string","pattern":"` + uuidPattern + `"}},
    "source_version_ids":{"type":"array","maxItems":100,"uniqueItems":true,"items":{"type":"string","pattern":"` + uuidPattern + `"}}
  }
}`

const searchKnowledgeOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["index_version_id","effective_mode","items","degradations"],
  "properties":{
    "index_version_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "effective_mode":{"type":"string","enum":["keyword","semantic","hybrid"]},
    "items":{"type":"array","maxItems":100,"items":{"type":"object","additionalProperties":false,
      "required":["citation_id","chunk_id","source_version_id","source_span_id","content_hash","rank","snippet"],
      "properties":{
        "citation_id":{"type":"string","minLength":1,"maxLength":128},
        "chunk_id":{"type":"string","pattern":"` + uuidPattern + `"},
        "source_version_id":{"type":"string","pattern":"` + uuidPattern + `"},
        "source_span_id":{"type":"string","pattern":"` + uuidPattern + `"},
        "content_hash":{"type":"string","pattern":"` + hash64Pattern + `"},
        "rank":{"type":"integer","minimum":1,"maximum":100},
        "snippet":{"type":"string","minLength":1,"maxLength":4096}
      }}},
    "degradations":{"type":"array","maxItems":16,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":64}}
  }
}`

const readSourceInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["source_version_id","source_span_id"],
  "properties":{
    "source_version_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "source_span_id":{"type":"string","pattern":"` + uuidPattern + `"}
  }
}`

const readSourceOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["source_version_id","source_span_id","content_hash","excerpt"],
  "properties":{
    "source_version_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "source_span_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "content_hash":{"type":"string","pattern":"` + hash64Pattern + `"},
    "excerpt":{"type":"string","minLength":1,"maxLength":262144}
  }
}`

const readDocumentInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["document_id"],
  "properties":{"document_id":{"type":"string","pattern":"` + uuidPattern + `"}}
}`

const readDocumentOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["document_id","document_version_id","content_hash","title","text"],
  "properties":{
    "document_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "document_version_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "content_hash":{"type":"string","pattern":"` + hash64Pattern + `"},
    "title":{"type":"string","minLength":1,"maxLength":1024},
    "text":{"type":"string","maxLength":1048576}
  }
}`

const fetchWebPageInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["url"],
  "properties":{"url":{"type":"string","minLength":1,"maxLength":8192,"format":"uri"}}
}`

const fetchWebPageOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["final_url","fetched_at","content_type","byte_count","content_hash","text","untrusted_data"],
  "properties":{
    "final_url":{"type":"string","minLength":1,"maxLength":8192,"format":"uri"},
    "fetched_at":{"type":"string","format":"date-time"},
    "content_type":{"type":"string","enum":["text/plain","text/html"]},
    "byte_count":{"type":"integer","minimum":0,"maximum":1048576},
    "content_hash":{"type":"string","pattern":"` + hash64Pattern + `"},
    "text":{"type":"string","maxLength":1048576},
    "untrusted_data":{"const":true}
  }
}`

const validateCitationInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["citations"],
  "properties":{"citations":{"type":"array","minItems":1,"maxItems":500,"uniqueItems":true,"items":{"$ref":"#/$defs/citation"}}},
  "$defs":{"citation":{"type":"object","additionalProperties":false,
    "required":["citation_id","index_version_id","chunk_id","source_version_id","source_span_id"],
    "properties":{
      "citation_id":{"type":"string","minLength":1,"maxLength":128},
      "index_version_id":{"type":"string","pattern":"` + uuidPattern + `"},
      "chunk_id":{"type":"string","pattern":"` + uuidPattern + `"},
      "source_version_id":{"type":"string","pattern":"` + uuidPattern + `"},
      "source_span_id":{"type":"string","pattern":"` + uuidPattern + `"}
    }}}
}`

const validateCitationOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["results"],
  "properties":{"results":{"type":"array","minItems":1,"maxItems":500,"items":{"type":"object","additionalProperties":false,
    "required":["citation_id","index_version_id","chunk_id","source_version_id","source_span_id","valid","reason_code"],
    "properties":{
      "citation_id":{"type":"string","minLength":1,"maxLength":128},
      "index_version_id":{"type":"string","pattern":"` + uuidPattern + `"},
      "chunk_id":{"type":"string","pattern":"` + uuidPattern + `"},
      "source_version_id":{"type":"string","pattern":"` + uuidPattern + `"},
      "source_span_id":{"type":"string","pattern":"` + uuidPattern + `"},
      "valid":{"type":"boolean"},
      "reason_code":{"type":"string","enum":["OK","CITATION_UNRESOLVABLE","EVIDENCE_INELIGIBLE","BINDING_MISMATCH"]}
    }}}}
}`

const calculateDiffInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["before","after"],
  "properties":{
    "before":{"type":"string","maxLength":524288},
    "after":{"type":"string","maxLength":524288}
  }
}`

const calculateDiffOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["changed","before_hash","after_hash","patch"],
  "properties":{
    "changed":{"type":"boolean"},
    "before_hash":{"type":"string","pattern":"` + hash64Pattern + `"},
    "after_hash":{"type":"string","pattern":"` + hash64Pattern + `"},
    "patch":{"type":"string","maxLength":1048576}
  }
}`

const readGitStatusInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,"properties":{}
}`

const readGitStatusOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["branch","head","object_format","clean","staged_count","unstaged_count","untracked_count","conflict_count"],
  "properties":{
    "branch":{"type":"string","minLength":1,"maxLength":255},
    "head":{"type":"string","pattern":"` + gitOIDPattern + `"},
    "object_format":{"type":"string","enum":["sha1","sha256"]},
    "clean":{"type":"boolean"},
    "staged_count":{"type":"integer","minimum":0,"maximum":100000},
    "unstaged_count":{"type":"integer","minimum":0,"maximum":100000},
    "untracked_count":{"type":"integer","minimum":0,"maximum":100000},
    "conflict_count":{"type":"integer","minimum":0,"maximum":100000}
  }
}`

const applyApprovedPatchInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["writeback_execution_id"],
  "properties":{"writeback_execution_id":{"type":"string","pattern":"` + uuidPattern + `"}}
}`

const applyApprovedPatchOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["writeback_execution_id","result_ref","result_hash","status"],
  "properties":{
    "writeback_execution_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "result_ref":{"type":"string","minLength":1,"maxLength":256},
    "result_hash":{"type":"string","pattern":"` + hash64Pattern + `"},
    "status":{"const":"APPLIED"}
  }
}`

const createGitCommitInputSchema = applyApprovedPatchInputSchema

const createGitCommitOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["writeback_execution_id","result_ref","commit_oid","status"],
  "properties":{
    "writeback_execution_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "result_ref":{"type":"string","minLength":1,"maxLength":256},
    "commit_oid":{"type":"string","pattern":"` + gitOIDPattern + `"},
    "status":{"const":"COMMITTED"}
  }
}`

const rebuildIndexInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["reindex_delivery_id"],
  "properties":{"reindex_delivery_id":{"type":"string","pattern":"` + uuidPattern + `"}}
}`

const rebuildIndexOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["reindex_delivery_id","index_version_id","result_ref","status"],
  "properties":{
    "reindex_delivery_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "index_version_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "result_ref":{"type":"string","minLength":1,"maxLength":256},
    "status":{"const":"SUCCEEDED"}
  }
}`

const runRegressionEvaluationInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["dataset_id","index_version_id"],
  "properties":{
    "dataset_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "index_version_id":{"type":"string","pattern":"` + uuidPattern + `"}
  }
}`

const runRegressionEvaluationOutputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["evaluation_run_id","result_ref","passed","status"],
  "properties":{
    "evaluation_run_id":{"type":"string","pattern":"` + uuidPattern + `"},
    "result_ref":{"type":"string","minLength":1,"maxLength":256},
    "passed":{"type":"boolean"},
    "status":{"type":"string","enum":["PASSED","FAILED"]}
  }
}`
