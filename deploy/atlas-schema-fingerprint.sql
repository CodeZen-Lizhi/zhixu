-- Stable catalog fingerprint supplements Atlas v1.2.2 schema diff with
-- PostgreSQL owner, ACL, function, trigger, policy, and other catalog facts.
WITH objects(object_key) AS (
    SELECT format('schema|%s|%s', nspname, nspowner::regrole::text)
    FROM pg_namespace
    WHERE nspname NOT LIKE 'pg_%'
      AND nspname <> 'information_schema'

    UNION ALL

    SELECT format(
        'schema_acl|%s|%s|%s|%s|%s',
        n.nspname,
        CASE WHEN acl.grantor = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(acl.grantor) END,
        CASE WHEN acl.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(acl.grantee) END,
        acl.privilege_type,
        acl.is_grantable
    )
    FROM pg_namespace n
    CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl, acldefault('n', n.nspowner))) AS acl
    WHERE n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'

    UNION ALL

    SELECT format('extension|%s|%s|%s', e.extname, e.extversion, n.nspname)
    FROM pg_extension e
    JOIN pg_namespace n ON n.oid = e.extnamespace
    WHERE e.extname <> 'plpgsql'

    UNION ALL

    SELECT format(
        'function|%s|%s|%s|%s|%s|%s|%s|%s',
        n.nspname,
        p.proname,
        pg_get_function_identity_arguments(p.oid),
        p.proowner::regrole::text,
        p.prokind,
        p.provolatile,
        p.prosecdef,
        md5(pg_get_functiondef(p.oid))
    )
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE p.prokind IN ('f', 'p')
      AND n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'

    UNION ALL

    SELECT format(
        'trigger|%s|%s|%s|%s|%s',
        n.nspname,
        c.relname,
        t.tgname,
        t.tgenabled::text,
        md5(pg_get_triggerdef(t.oid, true))
    )
    FROM pg_trigger t
    JOIN pg_class c ON c.oid = t.tgrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE NOT t.tgisinternal
      AND n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'

    UNION ALL

    SELECT format(
        'view|%s|%s|%s|%s|%s',
        n.nspname,
        c.relname,
        c.relkind::text,
        COALESCE(array_to_string(c.reloptions, ','), ''),
        md5(pg_get_viewdef(c.oid, true))
    )
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.relkind IN ('v', 'm')
      AND n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'

    UNION ALL

    SELECT format(
        'sequence|%s|%s|%s|%s|%s|%s|%s|%s',
        n.nspname,
        c.relname,
        s.seqstart::text,
        s.seqincrement::text,
        s.seqmin::text,
        s.seqmax::text,
        s.seqcache::text,
        s.seqcycle::text
    )
    FROM pg_sequence s
    JOIN pg_class c ON c.oid = s.seqrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'

    UNION ALL

    SELECT format(
        'enum|%s|%s|%s',
        n.nspname,
        t.typname,
        string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder)
    )
    FROM pg_type t
    JOIN pg_namespace n ON n.oid = t.typnamespace
    JOIN pg_enum e ON e.enumtypid = t.oid
    WHERE n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'
    GROUP BY n.nspname, t.typname

    UNION ALL

    SELECT format(
        'relation|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s',
        n.nspname,
        c.relname,
        c.relowner::regrole::text,
        c.relkind::text,
        c.relpersistence,
        c.relispartition,
        c.relrowsecurity,
        c.relforcerowsecurity,
        COALESCE(array_to_string(c.reloptions, ','), ''),
        COALESCE(am.amname, ''),
        COALESCE(pg_get_expr(c.relpartbound, c.oid), ''),
        COALESCE(pg_get_partkeydef(c.oid), '')
    )
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    LEFT JOIN pg_am am ON am.oid = c.relam
    WHERE n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'
      AND c.relkind IN ('r', 'p', 'v', 'm', 'S')

    UNION ALL

    SELECT format(
        'relation_acl|%s|%s|%s|%s|%s|%s',
        n.nspname,
        c.relname,
        CASE WHEN acl.grantor = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(acl.grantor) END,
        CASE WHEN acl.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(acl.grantee) END,
        acl.privilege_type,
        acl.is_grantable
    )
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    CROSS JOIN LATERAL aclexplode(c.relacl) AS acl
    WHERE c.relacl IS NOT NULL
      AND n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'
      AND c.relkind IN ('r', 'p', 'v', 'm', 'S')

    UNION ALL

    SELECT format(
        'column|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s',
        n.nspname,
        c.relname,
        a.attname,
        format_type(a.atttypid, a.atttypmod),
        a.attnotnull,
        a.attidentity,
        a.attgenerated,
        COALESCE(cn.nspname, ''),
        COALESCE(co.collname, ''),
        a.attstorage,
        a.attcompression,
        COALESCE(pg_get_expr(d.adbin, d.adrelid), '')
    )
    FROM pg_attribute a
    JOIN pg_class c ON c.oid = a.attrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
    LEFT JOIN pg_collation co ON co.oid = a.attcollation
    LEFT JOIN pg_namespace cn ON cn.oid = co.collnamespace
    WHERE a.attnum > 0
      AND NOT a.attisdropped
      AND n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'
      AND c.relkind IN ('r', 'p', 'v', 'm')

    UNION ALL

    SELECT format(
        'column_acl|%s|%s|%s|%s|%s|%s|%s',
        n.nspname,
        c.relname,
        a.attname,
        CASE WHEN acl.grantor = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(acl.grantor) END,
        CASE WHEN acl.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(acl.grantee) END,
        acl.privilege_type,
        acl.is_grantable
    )
    FROM pg_attribute a
    JOIN pg_class c ON c.oid = a.attrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    CROSS JOIN LATERAL aclexplode(a.attacl) AS acl
    WHERE a.attacl IS NOT NULL
      AND a.attnum > 0
      AND NOT a.attisdropped
      AND n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'
      AND c.relkind IN ('r', 'p', 'v', 'm')

    UNION ALL

    SELECT format(
        'function_acl|%s|%s|%s|%s|%s|%s|%s',
        n.nspname,
        p.proname,
        pg_get_function_identity_arguments(p.oid),
        CASE WHEN acl.grantor = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(acl.grantor) END,
        CASE WHEN acl.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(acl.grantee) END,
        acl.privilege_type,
        acl.is_grantable
    )
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace
    CROSS JOIN LATERAL aclexplode(p.proacl) AS acl
    WHERE p.proacl IS NOT NULL
      AND n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'

    UNION ALL

    SELECT format(
        'index|%s|%s|%s',
        n.nspname,
        c.relname,
        md5(pg_get_indexdef(i.indexrelid))
    )
    FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'

    UNION ALL

    SELECT format(
        'constraint|%s|%s|%s|%s|%s|%s',
        n.nspname,
        c.relname,
        con.conname,
        con.contype,
        con.convalidated,
        md5(pg_get_constraintdef(con.oid, true))
    )
    FROM pg_constraint con
    JOIN pg_class c ON c.oid = con.conrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'
      -- PostgreSQL 18 exposes NOT NULL as contype='n'. Column entries above
      -- already fingerprint attnotnull, while generated constraint names can
      -- differ after an otherwise equivalent table rename.
      AND con.contype <> 'n'

    UNION ALL

    SELECT format(
        'policy|%s|%s|%s|%s|%s|%s|%s',
        n.nspname,
        c.relname,
        pol.polname,
        pol.polpermissive,
        pol.polcmd,
        COALESCE((
            SELECT string_agg(role_name, ',' ORDER BY role_name)
            FROM (
                SELECT CASE
                    WHEN role_oid = 0 THEN 'PUBLIC'
                    ELSE pg_get_userbyid(role_oid)
                END AS role_name
                FROM unnest(pol.polroles) AS role_oid
            ) AS policy_roles
        ), ''),
        md5(COALESCE(pg_get_expr(pol.polqual, pol.polrelid), '') || E'\n' || COALESCE(pg_get_expr(pol.polwithcheck, pol.polrelid), ''))
    )
    FROM pg_policy pol
    JOIN pg_class c ON c.oid = pol.polrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname NOT LIKE 'pg_%'
      AND n.nspname <> 'information_schema'

    UNION ALL

    SELECT format(
        'event_trigger|%s|%s|%s|%s',
        evtname,
        evtevent,
        evtenabled,
        evtfoid::regproc::text
    )
    FROM pg_event_trigger
    WHERE evtname NOT LIKE 'pg_%'
)
SELECT object_key
FROM objects
ORDER BY object_key;
