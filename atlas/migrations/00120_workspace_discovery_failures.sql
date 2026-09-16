-- Observed discovery failures are scoped to an immutable physical root binding.
CREATE TABLE core.workspace_discovery_failure (
 workspace_id uuid NOT NULL REFERENCES core.workspace(id),
 binding_version bigint NOT NULL CHECK (binding_version >= 0),
 relative_path text NOT NULL CHECK (octet_length(relative_path) BETWEEN 1 AND 1024
  AND relative_path !~ '(^/|/$|//|(^|/)\.{1,2}(/|$)|[[:cntrl:]]|\\|:)'
  AND relative_path !~ '(^|/)(\.git|\.knowledge|tmp|\.tmp)(/|$)'),
 stage text NOT NULL CHECK (stage IN ('WALK','OBSERVE','REGISTER')),
 error_code text NOT NULL,
 status text NOT NULL CHECK (status IN ('FAILED','RECOVERED')),
 failure_count bigint NOT NULL CHECK (failure_count > 0),
 last_failed_at timestamptz NOT NULL,
 recovered_at timestamptz,
 PRIMARY KEY (workspace_id,binding_version,relative_path),
 CHECK ((stage='WALK' AND error_code='DIRECTORY_READ_FAILED') OR
        (stage='OBSERVE' AND error_code='FILE_OBSERVATION_FAILED') OR
        (stage='REGISTER' AND error_code='SOURCE_REGISTRATION_FAILED')),
 CHECK ((status='FAILED' AND recovered_at IS NULL) OR
        (status='RECOVERED' AND recovered_at IS NOT NULL AND recovered_at>=last_failed_at))
);
