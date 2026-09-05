import { App, Button, Input, Modal, Space, Table, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { Copy, KeyRound, Plus } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import { createApiKey, deleteApiKey, listApiKeys, type ApiKeyItem } from "@/lib/api-keys";

export default function ApiKeysPane() {
    const { message, modal } = App.useApp();
    const [items, setItems] = useState<ApiKeyItem[]>([]);
    const [loading, setLoading] = useState(false);
    const [creating, setCreating] = useState(false);
    const [nameInput, setNameInput] = useState("");
    const [createdSecret, setCreatedSecret] = useState<string | null>(null);
    const [copied, setCopied] = useState(false);

    const load = async () => {
        setLoading(true);
        try {
            const data = await listApiKeys();
            setItems(data.items ?? []);
        } catch (e) {
            message.error(e instanceof Error ? e.message : "加载 API Key 列表失败");
        } finally {
            setLoading(false);
        }
    };

    useEffect(() => {
        void load();
    }, []);

    const handleCreate = async () => {
        const name = nameInput.trim();
        if (!name) {
            message.warning("请填写名称");
            return;
        }
        setCreating(true);
        try {
            const created = await createApiKey(name);
            setCreatedSecret(created.key);
            setItems((prev) => [created, ...prev]);
            setNameInput("");
            message.success("API Key 创建成功");
        } catch (e) {
            message.error(e instanceof Error ? e.message : "创建失败");
        } finally {
            setCreating(false);
        }
    };

    const handleDelete = (item: ApiKeyItem) => {
        modal.confirm({
            title: "吊销 API Key",
            content: `确定吊销「${item.name}」（${item.prefix}…）？使用该 Key 的外部智能体将立即无法调用。`,
            okText: "吊销",
            okButtonProps: { danger: true },
            cancelText: "取消",
            onOk: async () => {
                try {
                    await deleteApiKey(item.id);
                    setItems((prev) => prev.filter((it) => it.id !== item.id));
                    message.success("已吊销");
                } catch (e) {
                    message.error(e instanceof Error ? e.message : "吊销失败");
                }
            },
        });
    };

    const copySecret = async () => {
        if (!createdSecret) return;
        try {
            await navigator.clipboard.writeText(createdSecret);
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
        } catch {
            message.warning("复制失败，请手动选择复制");
        }
    };

    const columns: ColumnsType<ApiKeyItem> = useMemo(
        () => [
            { title: "名称", dataIndex: "name", key: "name" },
            {
                title: "Key",
                dataIndex: "prefix",
                key: "prefix",
                render: (prefix: string) => (
                    <Typography.Text code copyable={{ text: prefix }}>
                        {prefix}…
                    </Typography.Text>
                ),
            },
            {
                title: "状态",
                dataIndex: "status",
                key: "status",
                render: (status: ApiKeyItem["status"]) =>
                    status === "active" ? <Tag color="green">启用</Tag> : <Tag color="default">停用</Tag>,
            },
            {
                title: "最近使用",
                dataIndex: "lastUsedAt",
                key: "lastUsedAt",
                render: (v: string | null) => (v ? new Date(v).toLocaleString() : "从未使用"),
            },
            {
                title: "创建时间",
                dataIndex: "createdAt",
                key: "createdAt",
                render: (v: string) => new Date(v).toLocaleString(),
            },
            {
                title: "操作",
                key: "actions",
                render: (_, item) => (
                    <Button type="link" danger size="small" onClick={() => handleDelete(item)}>
                        吊销
                    </Button>
                ),
            },
        ],
        [],
    );

    return (
        <div className="flex h-full flex-col">
            <div className="settings-pane-header">
                <div className="min-w-0">
                    <h2>API Key 管理</h2>
                    <p>外部智能体 / CLI 通过网关调用画布时使用；费用按画布后端真实定价从你的账户扣除。</p>
                </div>
            </div>

            <div className="settings-section">
                <Space.Compact style={{ width: "100%" }} className="mb-4">
                    <Input
                        placeholder="例如：我的剪辑脚本客户端"
                        value={nameInput}
                        onChange={(e) => setNameInput(e.target.value)}
                        onPressEnter={() => void handleCreate()}
                        allowClear
                    />
                    <Button type="primary" icon={<Plus className="size-4" />} loading={creating} onClick={() => void handleCreate()}>
                        创建
                    </Button>
                </Space.Compact>

                <Table<ApiKeyItem>
                    rowKey="id"
                    dataSource={items}
                    columns={columns}
                    loading={loading}
                    pagination={false}
                    size="middle"
                    locale={{ emptyText: "还没有 API Key，创建后即可供外部调用" }}
                />
            </div>

            <Modal
                open={!!createdSecret}
                onCancel={() => setCreatedSecret(null)}
                footer={[
                    <Button key="copy" type="primary" icon={<Copy className="size-4" />} onClick={() => void copySecret()}>
                        {copied ? "已复制" : "复制明文 Key"}
                    </Button>,
                    <Button key="done" onClick={() => setCreatedSecret(null)}>
                        我已保存
                    </Button>,
                ]}
                title={
                    <span className="inline-flex items-center gap-2">
                        <KeyRound className="size-4" /> API Key 已创建
                    </span>
                }
            >
                <p className="mb-2 text-sm text-amber-600">
                    明文 Key 仅显示这一次，关闭后无法再次查看，请立即复制并妥善保存。
                </p>
                <Typography.Paragraph copyable={{ text: createdSecret ?? "" }} code className="mb-0">
                    {createdSecret}
                </Typography.Paragraph>
            </Modal>
        </div>
    );
}
