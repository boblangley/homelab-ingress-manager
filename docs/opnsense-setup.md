# OPNsense setup

## Requirements

- **OPNsense 26.1 or newer.** Port forwards use the Destination NAT API
  (`/api/firewall/d_nat/*`), which arrived in 26.1. DNS overrides work on
  older releases, but the NAT calls will fail there.
- Unbound DNS as the resolver your clients use.

## Create an API user

1. **System > Access > Users**: add a user, e.g. `ingress-manager`. Give it a
   random password; it never logs in to the UI.
2. Under **Effective Privileges**, grant only what is needed:
   - the Unbound DNS settings page (host overrides) and the Unbound service
     page (to apply changes)
   - the Firewall Destination NAT (port forward) page
   Privilege names change between releases. Search the privilege list for
   "Unbound" and "Destination NAT" (or "Port Forward").
3. Under **API keys**, click **+**. OPNsense downloads a file holding `key`
   and `secret`. Use them as `OPNSENSE_API_KEY` and `OPNSENSE_API_SECRET`.

## Check access

From the Docker host:

```sh
curl -sk -u "$OPNSENSE_API_KEY:$OPNSENSE_API_SECRET" \
  -X POST -H 'Content-Type: application/json' -d '{}' \
  "$OPNSENSE_URL/api/unbound/settings/search_host_override"

curl -sk -u "$OPNSENSE_API_KEY:$OPNSENSE_API_SECRET" \
  -X POST -H 'Content-Type: application/json' -d '{}' \
  "$OPNSENSE_URL/api/firewall/d_nat/search_rule"
```

Both should return JSON with a `rows` array. A `401` means a wrong key or
secret; a `403` means a missing privilege.

## What you will see

Managed records have descriptions starting with `[homelab-ingress-manager]`
(or `[homelab-ingress-manager:<instance>]`), followed by the container name:

- **Services > Unbound DNS > Overrides**: one host override per site address.
- **Firewall > NAT > Destination NAT**: one rule per port forward group.

You can disable a managed record in the UI and it stays disabled as long as
its key fields are unchanged. If you delete it, the next reconcile recreates
it. To keep a record permanently, remove the tag from its description; it then
counts as yours and is never touched again.
