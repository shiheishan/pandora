# Config signing key rotation

Pandora nodes pin the Ed25519 configuration signing key obtained during
bootstrap. A replacement key is accepted only through an old-key-authenticated
transition returned by `GET /v1/nodes/config-signing-key`.

## Safe procedure

1. Deploy code containing the transition endpoint and client support while the
   existing `AEGIS_CONFIG_SIGNING_SEED` is still active.
2. Prepare a blue/green node-control deployment with the new seed in
   `AEGIS_CONFIG_SIGNING_SEED` and the old seed in
   `AEGIS_PREVIOUS_CONFIG_SIGNING_SEED`.
3. Atomically switch node-control traffic to that deployment. Do not keep old-
   current and new-current replicas in the same load-balancer pool after nodes
   begin adopting the new key.
4. Nodes authenticate the request with their node identity, verify the
   transition using their pinned old config key, atomically persist the new
   public key/KeyID, and then fetch the new Effective Release. A signer change
   advances `config_source_generation`; immutable releases are never re-signed
   in place.
5. Wait until every non-retired node reports the new key:

   ```sql
   SELECT id, name, config_signing_key_id, last_heartbeat_at
     FROM nodes
    WHERE status NOT IN ('destroyed','retired')
      AND serving_status <> 'retired'
      AND config_signing_key_id IS DISTINCT FROM '<new-key-id>';
   ```

   The result must remain empty for at least two heartbeat intervals.
6. Remove `AEGIS_PREVIOUS_CONFIG_SIGNING_SEED` and redeploy. Keep the old seed
   in offline recovery custody according to the incident-recovery policy; it
   must not remain in the running environment.

## Failure behavior

- Missing previous signer, unknown pinned KeyID, malformed proof, changed node
  identity, expired proof, or persistence failure all fail closed.
- The previous key can certify only the current public key; it never signs a
  new configuration release.
- Mixed old-current/new-current replicas are not a supported rollout topology.
  Use atomic blue/green cutover or a maintenance window.
