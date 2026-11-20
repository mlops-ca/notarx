import { useState } from "react";
import { Button, Input, Select, Space } from "antd";
import { useQuery } from "@tanstack/react-query";
import {
  addUserGroupMember,
  createUserGroup,
  listUsers,
  removeUserGroupMember,
  renameUserGroup,
  setUserGroupGlobalRole,
  type CreateUserGroup,
  type UserGroup,
  type UserGroupMember,
} from "../../../client";
import { Badge } from "../../../components/ui/Badge";
import { ConsoleTable } from "../../../components/ui/ConsolePrimitives";
import {
  EmptyState,
  ErrorBanner,
  Loading,
  Notice,
} from "../../../components/ui/Feedback";
import { Field, Pagination } from "../../../components/ui/Layout";
import { ConfirmDanger, Modal } from "../../../components/ui/Modal";
import { formatDate } from "../../../lib/format";
import { usePreferences } from "../../../lib/preferences";
import { unwrap } from "../../../lib/query/client";
import { isActiveUserPrincipal } from "../../../lib/accessPrincipals";
import { useIdentityProviders } from "../../settings/providers/providerQueries";
import { useUserGroupMemberPages } from "./userGroupQueries";

type GlobalRole = UserGroup["globalRole"];

/** Each write owns its form, busy state and error; the page hears only about success. */
function useWrite(onDone: () => Promise<void> | void) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await action();
      await onDone();
    } catch (nextError) {
      setError(nextError);
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, run, clear: () => setError(null) };
}

export function CreateUserGroupDialog({
  open,
  onClose,
  onDone,
}: {
  open: boolean;
  onClose: () => void;
  onDone: () => Promise<void> | void;
}) {
  const { t } = usePreferences();
  const write = useWrite(onDone);
  const [name, setName] = useState("");
  const [source, setSource] = useState<"local" | "claims">("local");
  const [providerId, setProviderId] = useState<string | undefined>();
  const [externalId, setExternalId] = useState("");
  const providers = useIdentityProviders();
  const complete =
    name.trim() !== "" &&
    (source === "local" ||
      (providerId !== undefined && externalId.trim() !== ""));
  const submit = () =>
    write.run(async () => {
      const body: CreateUserGroup = { displayName: name.trim(), source };
      if (source === "claims") {
        body.providerId = providerId;
        body.externalId = externalId.trim();
      }
      await unwrap(createUserGroup({ body }));
      setName("");
      setExternalId("");
    });
  return (
    <Modal
      open={open}
      title={t("userGroups.createTitle")}
      onClose={onClose}
      busy={write.busy}
      footer={
        <Space>
          <Button onClick={onClose} disabled={write.busy}>
            {t("userGroups.cancel")}
          </Button>
          <Button
            type="primary"
            loading={write.busy}
            disabled={!complete}
            onClick={() => void submit()}
          >
            {t("userGroups.create")}
          </Button>
        </Space>
      }
    >
      <div className="space-y-4">
        {write.error !== null && <ErrorBanner error={write.error} />}
        <Field label={t("userGroups.name")} hint={t("userGroups.nameHint")}>
          <Input
            value={name}
            maxLength={255}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("userGroups.source")} hint={t("userGroups.sourceHint")}>
          <Select<"local" | "claims">
            className="w-full"
            aria-label={t("userGroups.source")}
            value={source}
            onChange={setSource}
            options={[
              { value: "local", label: t("userGroups.source.local") },
              { value: "claims", label: t("userGroups.source.claims") },
            ]}
          />
        </Field>
        {source === "claims" && (
          <>
            <Field label={t("userGroups.provider")}>
              <Select<string>
                className="w-full"
                aria-label={t("userGroups.provider")}
                value={providerId}
                loading={providers.isPending}
                placeholder={t("userGroups.providerPlaceholder")}
                onChange={setProviderId}
                options={(providers.data ?? [])
                  .filter((provider) => provider.kind === "oidc")
                  .map((provider) => ({
                    value: provider.id,
                    label: provider.displayName,
                  }))}
              />
            </Field>
            <Field
              label={t("userGroups.externalId")}
              hint={t("userGroups.externalIdHint")}
            >
              <Input
                value={externalId}
                maxLength={255}
                onChange={(event) => setExternalId(event.target.value)}
              />
            </Field>
          </>
        )}
      </div>
    </Modal>
  );
}

export function RenameUserGroupDialog({
  group,
  onClose,
  onDone,
}: {
  group: UserGroup | null;
  onClose: () => void;
  onDone: () => Promise<void> | void;
}) {
  const { t } = usePreferences();
  const write = useWrite(onDone);
  const [draft, setDraft] = useState<{ id: string; name: string } | null>(null);
  const name =
    draft !== null && draft.id === group?.id
      ? draft.name
      : (group?.displayName ?? "");
  return (
    <Modal
      open={group !== null}
      title={t("userGroups.renameTitle", { name: group?.displayName ?? "" })}
      onClose={onClose}
      afterClose={write.clear}
      busy={write.busy}
      footer={
        <Space>
          <Button onClick={onClose} disabled={write.busy}>
            {t("userGroups.cancel")}
          </Button>
          <Button
            type="primary"
            loading={write.busy}
            disabled={name.trim() === "" || name.trim() === group?.displayName}
            onClick={() =>
              group &&
              void write.run(() =>
                unwrap(
                  renameUserGroup({
                    path: { groupId: group.id },
                    headers: { "If-Match": group.version },
                    body: { displayName: name.trim() },
                  }),
                ),
              )
            }
          >
            {t("userGroups.save")}
          </Button>
        </Space>
      }
    >
      <div className="space-y-4">
        {write.error !== null && <ErrorBanner error={write.error} />}
        <Field label={t("userGroups.name")} hint={t("userGroups.nameHint")}>
          <Input
            value={name}
            maxLength={255}
            onChange={(event) =>
              setDraft({ id: group?.id ?? "", name: event.target.value })
            }
          />
        </Field>
      </div>
    </Modal>
  );
}

const ROLES: GlobalRole[] = ["none", "member", "admin"];

export function GlobalRoleDialog({
  group,
  onClose,
  onDone,
}: {
  group: UserGroup | null;
  onClose: () => void;
  onDone: () => Promise<void> | void;
}) {
  const { t } = usePreferences();
  const write = useWrite(onDone);
  const [draft, setDraft] = useState<{ id: string; role: GlobalRole } | null>(
    null,
  );
  const role =
    draft !== null && draft.id === group?.id
      ? draft.role
      : (group?.globalRole ?? "none");
  const name = group?.displayName ?? "";
  const claims = group?.source === "claims";
  return (
    <Modal
      open={group !== null}
      title={t("userGroups.roleTitle", { name })}
      onClose={onClose}
      afterClose={write.clear}
      busy={write.busy}
      footer={
        <Space>
          <Button onClick={onClose} disabled={write.busy}>
            {t("userGroups.cancel")}
          </Button>
          <Button
            type="primary"
            danger={role === "admin"}
            loading={write.busy}
            disabled={claims || role === group?.globalRole}
            onClick={() =>
              group &&
              void write.run(() =>
                unwrap(
                  setUserGroupGlobalRole({
                    path: { groupId: group.id },
                    headers: { "If-Match": group.version },
                    body: { globalRole: role },
                  }),
                ),
              )
            }
          >
            {t("userGroups.save")}
          </Button>
        </Space>
      }
    >
      <div className="space-y-4">
        {write.error !== null && <ErrorBanner error={write.error} />}
        <p className="text-sm text-fg-tertiary">{t("userGroups.roleHint")}</p>
        {claims && (
          <Notice
            tone="info"
            closable={false}
            title={t("userGroups.claimsRoleNotice")}
          />
        )}
        <Field label={t("userGroups.globalRole")}>
          <Select<GlobalRole>
            className="w-full"
            aria-label={t("userGroups.globalRole")}
            value={role}
            disabled={claims}
            onChange={(next) => setDraft({ id: group?.id ?? "", role: next })}
            options={ROLES.map((value) => ({
              value,
              label: t(`userGroups.role.${value}`),
            }))}
          />
        </Field>
        {role === "admin" && role !== group?.globalRole && (
          <Notice
            tone="warning"
            closable={false}
            title={t("userGroups.adminWarningTitle")}
            description={t("userGroups.adminWarning", { name })}
          />
        )}
      </div>
    </Modal>
  );
}

export function UserGroupMembersDialog({
  group,
  onClose,
  onChanged,
}: {
  group: UserGroup | null;
  onClose: () => void;
  onChanged: () => Promise<void> | void;
}) {
  const { locale, t } = usePreferences();
  const groupId = group?.id ?? "";
  const membersQuery = useUserGroupMemberPages(groupId, group !== null);
  const usersQuery = useQuery({
    queryKey: ["identity", "user-groups", "member-choices"],
    queryFn: ({ signal }) =>
      unwrap(listUsers({ query: { limit: 200 }, signal })),
    enabled: group !== null,
  });
  const write = useWrite(async () => {
    await membersQuery.refetch();
    await onChanged();
  });
  const [userId, setUserId] = useState<string | undefined>();
  const [toRemove, setToRemove] = useState<UserGroupMember | null>(null);
  const members =
    membersQuery.data?.pages.flatMap((page) => page.items) ?? null;
  const memberIds = new Set((members ?? []).map((member) => member.userId));
  const choices = (usersQuery.data?.items ?? []).filter(
    (user) => isActiveUserPrincipal(user) && !memberIds.has(user.id),
  );
  const error = membersQuery.error ?? usersQuery.error ?? null;
  return (
    <Modal
      open={group !== null}
      title={t("userGroups.membersTitle", { name: group?.displayName ?? "" })}
      onClose={onClose}
      afterClose={() => {
        write.clear();
        setUserId(undefined);
      }}
      wide
      footer={<Button onClick={onClose}>{t("userGroups.close")}</Button>}
    >
      <div className="space-y-4">
        {error !== null && <ErrorBanner error={error} />}
        {write.error !== null && <ErrorBanner error={write.error} />}
        <Space.Compact className="w-full">
          <Select<string>
            className="w-full"
            aria-label={t("userGroups.addMember")}
            showSearch={{ optionFilterProp: "label" }}
            value={userId}
            loading={usersQuery.isPending}
            placeholder={t("userGroups.userPlaceholder")}
            onChange={setUserId}
            options={choices.map((user) => ({
              value: user.id,
              label: user.name,
            }))}
          />
          <Button
            type="primary"
            loading={write.busy}
            disabled={userId === undefined}
            onClick={() =>
              userId !== undefined &&
              void write.run(async () => {
                await unwrap(
                  addUserGroupMember({
                    path: { groupId },
                    body: { userId },
                  }),
                );
                setUserId(undefined);
              })
            }
          >
            {t("userGroups.addMember")}
          </Button>
        </Space.Compact>
        {members === null ? (
          <Loading label={t("userGroups.loadingMembers")} />
        ) : members.length === 0 ? (
          <EmptyState title={t("userGroups.noMembers")} />
        ) : (
          <ConsoleTable<UserGroupMember>
            rowKey="userId"
            dataSource={members}
            pagination={false}
            columns={[
              {
                title: t("userGroups.memberUser"),
                dataIndex: "userName",
                key: "user",
              },
              {
                title: t("userGroups.source"),
                key: "source",
                render: (_, member) => (
                  <Badge>{t(`userGroups.source.${member.source}`)}</Badge>
                ),
              },
              {
                title: t("userGroups.memberAdded"),
                key: "added",
                render: (_, member) => formatDate(member.createdAt, locale),
              },
              {
                title: t("userGroups.actions"),
                key: "actions",
                render: (_, member) => (
                  <Button size="small" onClick={() => setToRemove(member)}>
                    {t("userGroups.removeMember")}
                  </Button>
                ),
              },
            ]}
          />
        )}
        <Pagination
          hasMore={Boolean(membersQuery.hasNextPage)}
          loading={membersQuery.isFetchingNextPage}
          onMore={() => void membersQuery.fetchNextPage()}
          label={t("userGroups.loadMore")}
        />
      </div>
      <ConfirmDanger
        target={toRemove}
        title={t("userGroups.removeMemberTitle")}
        message={(member) =>
          t("userGroups.removeMemberMessage", {
            user: member.userName,
            group: group?.displayName ?? "",
          })
        }
        confirmLabel={t("userGroups.removeMember")}
        onConfirm={(member) =>
          unwrap(
            removeUserGroupMember({
              path: { groupId, userId: member.userId },
            }),
          ).then(() => {})
        }
        onClose={() => setToRemove(null)}
        onDone={() => void write.run(async () => {})}
      />
    </Modal>
  );
}
