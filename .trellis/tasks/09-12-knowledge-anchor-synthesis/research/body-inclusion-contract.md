# Active implementation contract

Domain/application types are now present. `SynthesisGenerationNote.PublicationID foundation.ID` with JSON `publication_id,omitempty`. `SynthesisItem.BodyReference *SynthesisBodyReference` with JSON `body_reference,omitempty`.

`domain.IncludeSynthesisPublishedItem(revision SynthesisRevision, publicationID, itemID, newItemID foundation.ID) (SynthesisOperation,error)` deep-copies exact item and original evidence, assigns new ID and reference; ADD_GAP with resolution is allowed only for BodyReference items, and must receive semantic review of its resolution. Reference fields: WorkspaceID, NoteID, RevisionID, PublicationID, ItemID, ProjectionHash (no ItemHash).

Constants in application: SynthesisBodyPromptVersion="v4", SynthesisBodySchemaVersion="v2". SynthesisRuntimeVersion remains v1. Root owns domain/application/workflow/synthesispostgres/postgres/migration changes. Worker body_inclusion_model owns adapter/agent only as dispatched.
