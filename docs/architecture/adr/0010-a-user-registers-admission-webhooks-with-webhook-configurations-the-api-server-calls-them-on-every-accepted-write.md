# ADR 0010: A user registers admission webhooks with webhook configurations, and the API server calls them on every accepted write

> Status: accepted
> Date: 2026-11-12

Drafted for garamsh/gaon#85, under goal garamsh/gaon#74. The design is the PM's, taken under the project owner's decisions in #74 (Kubernetes semantics, a gaon-native implementation, additive only, no namespaces) and recorded in #85 §Design. It places the order of a write relative to the authorization of [ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md) and the schema and pruning of [ADR 0009](./0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md). This ADR is the contract; serving it in the API server is a later issue of #74.

## Context

- **The API server validates a write by its own rules only.** A user cannot add a default or a policy of their own without a change to the API server, and a registered kind (ADR 0009) has only its schema.
- **A webhook runs outside the API server.** Its address, the trust the API server places in it and what happens when it does not answer are therefore part of the contract, as is the shape of the call it receives.
- **The semantics follow Kubernetes' documentation, read on 2026-11-12:**
  - "Dynamic Admission Control", <https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/>. The footer reads "Last modified October 31, 2026"; its links and feature-state notes name v1.37. The page is 154619 characters long. It was first fetched through a tool that returned a summary of the first 100000 characters and no field-level detail, so its HTML was fetched again and its text extracted; the 51522 characters of the extracted text, which is the whole page without its markup, were read directly. The pages it links to (the API reference of the configuration types, the admission-controller reference, the user guides) were not read.
  - What was taken from it:
    - a mutating webhook is called first and may change the object; the validating webhooks are called after the object is validated by the API server and may refuse; a webhook that must see the final object is a validating one;
    - a configuration holds webhooks, and each in one configuration has a unique name;
    - a webhook has `rules` that select requests by operation, API group, API version and resource, and a rule's `operations` are `CREATE`, `UPDATE`, `DELETE` or `*`; the resources include subresources such as `*/status`;
    - `clientConfig` may carry a `url`, whose scheme must be `https`, with no user or password, no fragment and no query; `caBundle` is a PEM bundle, base64 in the field;
    - `failurePolicy` is `Ignore` or `Fail`, `Fail` is the default, and an error calling the webhook is ignored or rejects the request accordingly; an error is a timeout, a connection failure, a non-2xx answer, a malformed answer, and for a mutating webhook an undecodable or unsupported patch;
    - `timeoutSeconds` is between 1 and 30 and defaults to 10; a timeout is a failed call and follows the failure policy;
    - the request is an `AdmissionReview` with `request.uid`, `kind`, `resource`, `subResource`, `name`, `operation`, `userInfo` (`username`, `groups`), `object` and `oldObject`; the webhook answers with HTTP 200, `application/json` and an `AdmissionReview` whose `response` has at least `uid` and `allowed`;
    - `response.status` carries a `code` and a `message`, and the page's example of refusing sets 403;
    - a mutating webhook's `response` may carry `patchType` `JSONPatch`, the only one, and a base64 `patch` that is an array of JSON Patch operations.
  - What the page does not state, so that it is not taken as Kubernetes' behaviour:
    - the form of a webhook's `name` beyond its uniqueness, and a bound on the number of webhooks;
    - the order in which configurations are called, and the order of the webhooks in one configuration;
    - whether each mutating webhook sees the patch of the one before it;
    - whether a patch may change `apiVersion`, `kind` or `metadata.name`;
    - the HTTP status and the reason of a write refused for a failed call, and whether a refusal is `Forbidden`;
    - what the API server does when the webhook answers 200 with a `uid` that is not the request's.
  - Where gaon departs from Kubernetes on purpose:
    - **`url` only.** A webhook is an address, never a service reference, because there are no Services or namespaces to resolve one against; and `caBundle` is required, because Kubernetes lets the system trust roots stand in for it and gaon's API server has no such trust to offer a webhook on a private network.
    - **No `namespaceSelector` and `objectSelector`**: there are no namespaces, and an object selector would need the labels of both objects on every call.
    - **No `sideEffects`.** Kubernetes uses it for dry-run requests, which gaon does not have.
    - **No `reinvocationPolicy`, `matchPolicy`, `matchConditions` or `admissionReviewVersions`.** A webhook is called once per write and there is one version of the review.
    - **`scope` is not a field of a rule**; every resource is cluster-scoped.
    - **A refusal is `Forbidden` 403 whatever `status.code` the webhook sets.** Kubernetes returns the webhook's status object to the user, so a webhook may answer with any code; gaon fixes the reason so that a client keys on one.
    - **A write refused for a failed call is `InternalError` 500.** The page does not give the status, and this is a new reason in the error object.
    - **The `AdmissionReview` is not served by any path**; it is the body of a call the API server makes, not a resource.

## Decision

**A `MutatingWebhookConfiguration` or a `ValidatingWebhookConfiguration` in `admissionregistration.gaon.garam.sh/v1alpha1` registers webhooks the API server calls with an `AdmissionReview` on every accepted write, after authorization and around the API server's own validation, in an order the contract fixes.**

1. **Resources.** `MutatingWebhookConfiguration` and `ValidatingWebhookConfiguration`, root `/apis/admissionregistration.gaon.garam.sh/v1alpha1`, cluster-scoped, each with `PUT`, `GET` of one and of a list, `GET ?watch=true` and `DELETE`. They are authorized as the resources `mutatingwebhookconfigurations` and `validatingwebhookconfigurations` in that group, by ADR 0008's rules; `gaon:admin`'s `*` covers both, and the bootstrap roles name neither.
2. **Reserved groups.** `admissionregistration.gaon.garam.sh` and `admission.gaon.garam.sh` may not be the `group` of a CustomResourceDefinition, in `lib/api` and in [resource-api.md](../resource-api.md), on the rule of ADR 0009, item 3.
3. **A webhook** has these fields.
   - `name`: unique within its configuration, a domain of at least three lowercase DNS labels and at most 253 characters.
   - `clientConfig`: `url`, an `https` URL with a host, no user or password, no query and no fragment; and `caBundle`, a PEM bundle of at least one certificate, base64 in JSON, required. The API server trusts the webhook's certificate only through this bundle.
   - `rules`: at least one. Each has `operations` (`CREATE`, `UPDATE`, `DELETE` or `*`), `apiGroups`, `apiVersions` and `resources`, each non-empty. A group or version is a name or `*`. A resource is `*`, `*/*`, a resource, or `resource/subresource`, with `*` for either part: `*` matches every resource and no subresource, `*/*` every resource and subresource.
   - `failurePolicy`: `Fail`, the default, or `Ignore`.
   - `timeoutSeconds`: 1 to 30, 10 when absent.
   - There is no namespace selector, object selector, `sideEffects` or `reinvocationPolicy`, for the reasons in §Context. Each can be added later as a field with a default that keeps the present behaviour.
4. **`AdmissionReview`** in `admission.gaon.garam.sh/v1alpha1`, a body and not a resource. A call carries `request`: `uid`, `kind` and `resource` (each with group and version), `subResource`, `name`, `operation`, `userInfo` (`username`, `groups`), `object` and `oldObject`. The answer carries `response`: `uid`, `allowed`, `status` (`code`, `message`), and, for a mutating webhook that allows, `patchType` `JSONPatch` and `patch`, a base64 RFC 6902 array. A patch of a refusal is invalid. The webhook is called with `POST`, `Content-Type: application/json`.
5. **Which writes are sent.** Every write accepted after authorization.
   - A `PUT` without a version is `CREATE`; with one it is `UPDATE`. A write to `/status` is `UPDATE` with `subResource` `status`. `POST /machines/{name}/binding` is `CREATE` on `machines/binding`. A `DELETE` is `DELETE`, with `oldObject` set and `object` absent. `object` is absent for a `DELETE` and `oldObject` for a `CREATE`.
   - Writes to the two configuration kinds are never sent, so a broken webhook cannot lock out its own repair. Reads are never sent.
6. **Order of a write.** Authentication; authorization; the mutating webhooks; the API server's own validation, with a custom resource's schema and pruning (ADR 0009); the validating webhooks; the Store.
   - Mutating configurations are called by name, the webhooks of one configuration in their listed order, and each sees the object as the previous webhook's patch left it. The validating webhooks are called in the same order, each seeing the final object.
   - A patch that changes `apiVersion`, `kind`, `metadata.name` or `metadata.resourceVersion` is a failed call.
7. **Failure.**
   - `allowed: false` refuses the write as `Forbidden` 403 whatever `status.code` says; the message names the webhook and carries `status.message`.
   - A failed call is a timeout, a TLS or connection error, a status other than 200, a body that does not decode as an `AdmissionReview`, a `uid` that is not the request's, or a bad patch. Under `Fail` it refuses the write; under `Ignore` the webhook is skipped, and any patch it returned is not applied.
   - A write refused for a failed call is the new reason `InternalError`, HTTP 500, which `lib/api` carries as `ReasonInternalError` and `ErrInternalError`.
8. **Client.** `lib/api` has the two configuration types and their lists with `Validate`, the methods `Put`, `Get`, `List`, `Watch` and `Delete` of each on `Client`, which is configured with the root of its group as the other groups' clients are, and the `AdmissionReview` types with `Validate` and `ValidateResponseTo`. Matching a request against `rules` and calling a webhook are the API server's, in the next issue.

## Consequences

- **A policy or a default can be added without a change to the API server.** The webhook is a Go program that decodes an `AdmissionReview` from `lib/api` and encodes its answer.
- **A webhook can refuse every write that reaches it.** Its own repair is not sent to it, but a webhook that refuses every other kind, including the `Lease` writes of Node Agents, can stall the cluster until its configuration is deleted or its failure policy is `Ignore`. The default is `Fail`, as Kubernetes' is, and the choice is the registrant's.
- **Every accepted write waits for each matching webhook in turn**, up to its timeout. The bound is `timeoutSeconds` of each, and the sum over a write is not bounded by the contract.
- **A refusal is one reason for a client to key on.** A webhook cannot answer 404 or 409 to a user; it can write the cause in the message.
- **`InternalError` is new.** A client that maps the error reasons exhaustively adds it; the reason is outside what Kubernetes' page states, and the departure is recorded here.
- **A webhook's `caBundle` is the whole trust.** The API server does not consult the system roots for a webhook, so a webhook behind a public certificate still carries the bundle that signs it.
- **Rejected: a service reference.** There is no Service resource to resolve it, and it would tie this contract to one that is not written.
- **Rejected: selectors by namespace and by object labels.** There are no namespaces, and the label selector needs both objects on every call. A rule on resource and operation selects what the first uses need.
- **Rejected: a fixed HTTP status from the webhook.** It lets a webhook answer with a reason the API server has no meaning for, such as `Expired`.
- **Rejected: sending a write to a configuration to the webhooks.** A webhook that refuses it cannot be removed without editing the Store.
- **Nothing is enforced until the API server serves it.** The contract and the library validate; calling the webhooks, applying a patch and the order above are the next issue, and until it lands the documents describe what the server will do.

## Signs the decision was wrong

- Registrants need to select by namespace-like groupings or by labels, and each webhook receives writes it must discard.
- Users want a webhook to run once but see the final object, and ask for a reinvocation policy.
- Webhooks need a status other than 403 to speak to a client, and ask the API server to pass `status.code` through.
- A stalled cluster from a webhook with `Fail` is common enough that operators keep editing the Store, which says the default or the exemption of the configuration kinds is too narrow.
- A webhook behind a service of its own cannot be addressed by one `https` URL.
