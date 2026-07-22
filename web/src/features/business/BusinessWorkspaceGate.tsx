import { Link } from "react-router-dom";

import { UnavailableState } from "../../shared/ui";

export const BusinessWorkspaceGate = ({
  title = "先连接 Workspace",
  description = "该页面只读取当前 Workspace 的真实业务事实；这里不会使用本地样例或缓存数据代替。",
}: {
  title?: string;
  description?: string;
}) => (
  <div className="page-stack">
    <UnavailableState title={title} description={description} />
    <Link className="ui-button ui-button--primary" to="/workspace">
      连接或切换 Workspace
    </Link>
  </div>
);
