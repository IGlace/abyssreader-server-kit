<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Resolve a source definition

The [definition schema](definition.schema.json) applies to the resolved JSON.
A family and a source are partial JSON objects. Resolve the source over its
family before validating the result.

1. Start with the family. For each source property, merge it recursively if
   both values are objects. Otherwise use the source value, including arrays,
   scalars and null.
2. The top-level `hosts` array is the exception. Append the source's hosts to
   the family's list, so a source can add hosts shared by other members.
3. Substitute `{{params.NAME}}` in string values with values from the merged
   `params` object. A missing parameter is an error. Substitute non-string
   values as JSON text. Keep runtime references such as `{{baseUrl}}` intact.
4. Remove duplicate hosts after substitution, keeping the first occurrence.
   Validate the resolved JSON. The size limit is 2 MiB.

A host entry is a public DNS name or a single-label wildcard such as
`*.cdn.example.com`. The wildcard matches `a.cdn.example.com` and does not
match the apex, deeper subdomains or a public suffix. The app checks the
destination address and each redirect when it runs the definition.
