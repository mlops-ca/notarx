import { useState } from "react";
import { PlusOutlined } from "@ant-design/icons";
import { Button, Space } from "antd";
import type { ColumnsType } from "antd/es/table";
import { deleteUserGroup, type UserGroup } from "../../../client";
import { Badge } from "../../../components/ui/Badge";
import { ConsoleTable } from "../../../components/ui/ConsolePrimitives";
import {
  EmptyState,
  ErrorBanner,
  Loading,
} from "../../../components/ui/Feedback";
import { Card, PageHeader, Pagination } from "../../../components/ui/Layout";
import { ConfirmDanger, useDisclosure } from "../../../components/ui/Modal";
import { roleTone } from "../users/userPresentation";
import { usePreferences } from "../../../lib/preferences";
import { unwrap } from "../../../lib/query/client";
import {
  CreateUserGroupDialog,
  GlobalRoleDialog,
  RenameUserGroupDialog,
  UserGroupMembersDialog,
} from "./UserGroupDialogs";
import { useInvalidateUserGroups, useUserGroupPages } from "./userGroupQueries";

export function UserGroupsPage() {
  const { t } = usePreferences();
  const query = useUserGroupPages();
  const invalidate = useInvalidateUserGroups();
  const createDialog = useDisclosure();
  const [actionError, setActionError] = useState<unknown>(null);
  const [renaming, setRenaming] = useState<UserGroup | null>(null);
  const [roleOf, setRoleOf] = useState<UserGroup | null>(null);
  const [membersOf, setMembersOf] = useState<UserGroup | null>(null);
  const [toDelete, setToDelete] = useState<UserGroup | null>(null);
  const groups = query.data?.pages.flatMap((page) => page.items) ?? null;
  const error = actionError ?? query.error ?? null;

  const refresh = () => {
    setActionError(null);
    return invalidate();
  };
  const columns: ColumnsType<UserGroup> = [
    { title: t("userGroups.name"), dataIndex: "displayName", key: "name" },
    {
      title: t("userGroups.source"),
      key: "source",
      render: (_, group) => (
        <Badge>{t(`userGroups.source.${group.source}`)}</Badge>
      ),
    },
    {
      title: t("userGroups.globalRole"),
      key: "role",
      render: (_, group) => (
        <Badge tone={roleTone(group.globalRole)}>
          {t(`userGroups.role.${group.globalRole}`)}
        </Badge>
      ),
    },
    {
      title: t("userGroups.actions"),
      key: "actions",
      render: (_, group) => (
        <Space wrap>
          <Button size="small" onClick={() => setMembersOf(group)}>
            {t("userGroups.members")}
          </Button>
          <Button size="small" onClick={() => setRenaming(group)}>
            {t("userGroups.rename")}
          </Button>
          <Button size="small" onClick={() => setRoleOf(group)}>
            {t("userGroups.setRole")}
          </Button>
          <Button size="small" danger onClick={() => setToDelete(group)}>
            {t("userGroups.delete")}
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <div className="ag-page-stack">
      <PageHeader
        title={t("userGroups.title")}
        description={t("userGroups.description")}
        actions={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={createDialog.show}
          >
            {t("userGroups.new")}
          </Button>
        }
      />
      {error !== null && (
        <ErrorBanner error={error} onRetry={() => void refresh()} />
      )}
      <Card>
        {groups === null ? (
          <div className="p-8">
            <Loading label={t("userGroups.loading")} />
          </div>
        ) : groups.length === 0 ? (
          <div className="p-8">
            <EmptyState
              title={t("userGroups.empty")}
              hint={t("userGroups.emptyHint")}
            />
          </div>
        ) : (
          <ConsoleTable<UserGroup>
            rowKey="id"
            dataSource={groups}
            columns={columns}
            pagination={false}
            scroll={{ x: 700 }}
          />
        )}
        <Pagination
          hasMore={Boolean(query.hasNextPage)}
          loading={query.isFetchingNextPage}
          onMore={() => void query.fetchNextPage()}
          label={t("userGroups.loadMore")}
        />
      </Card>
      <CreateUserGroupDialog
        open={createDialog.open}
        onClose={createDialog.hide}
        onDone={async () => {
          createDialog.hide();
          await refresh();
        }}
      />
      <RenameUserGroupDialog
        group={renaming}
        onClose={() => setRenaming(null)}
        onDone={async () => {
          setRenaming(null);
          await refresh();
        }}
      />
      <GlobalRoleDialog
        group={roleOf}
        onClose={() => setRoleOf(null)}
        onDone={async () => {
          setRoleOf(null);
          await refresh();
        }}
      />
      <UserGroupMembersDialog
        group={membersOf}
        onClose={() => setMembersOf(null)}
        onChanged={refresh}
      />
      <ConfirmDanger
        target={toDelete}
        title={t("userGroups.deleteTitle")}
        message={(group) =>
          t("userGroups.deleteMessage", { name: group.displayName })
        }
        confirmLabel={t("userGroups.delete")}
        onConfirm={(group) =>
          unwrap(deleteUserGroup({ path: { groupId: group.id } })).then(
            () => {},
          )
        }
        onClose={() => setToDelete(null)}
        onDone={() => void refresh()}
        onError={setActionError}
      />
    </div>
  );
}
