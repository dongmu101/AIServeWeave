/** Fixed acceptance copy is independent of runtime catalogue lookup. / 固定验收文案不读取运行时词典。 */
export const tenantEntries = [
  {
    "path": "/console",
    "headings": {
      "zh-CN": "概览",
      "zh-TW": "總覽",
      "en-US": "Overview",
      "ja-JP": "概要",
      "ko-KR": "개요",
      "de-DE": "Übersicht",
      "fr-FR": "Vue d’ensemble",
      "es-ES": "Resumen"
    }
  },
  {
    "path": "/console/users",
    "headings": {
      "zh-CN": "用户",
      "zh-TW": "使用者",
      "en-US": "Users",
      "ja-JP": "ユーザー",
      "ko-KR": "사용자",
      "de-DE": "Benutzer",
      "fr-FR": "Utilisateurs",
      "es-ES": "Usuarios"
    }
  },
  {
    "path": "/console/keys",
    "headings": {
      "zh-CN": "API Key",
      "zh-TW": "API Key",
      "en-US": "API Key",
      "ja-JP": "API Key",
      "ko-KR": "API Key",
      "de-DE": "API Key",
      "fr-FR": "API Key",
      "es-ES": "API Key"
    }
  },
  {
    "path": "/console/audit",
    "headings": {
      "zh-CN": "管理审计",
      "zh-TW": "管理稽核",
      "en-US": "Administrative audit",
      "ja-JP": "管理監査",
      "ko-KR": "관리 감사",
      "de-DE": "Verwaltungs-Audit",
      "fr-FR": "Audit de gestion",
      "es-ES": "Auditoría administrativa"
    }
  },
  {
    "path": "/console/requests",
    "headings": {
      "zh-CN": "请求检索",
      "zh-TW": "請求查詢",
      "en-US": "Request search",
      "ja-JP": "リクエスト検索",
      "ko-KR": "요청 검색",
      "de-DE": "Anfragesuche",
      "fr-FR": "Recherche de requêtes",
      "es-ES": "Buscar solicitudes"
    }
  },
  {
    "path": "/console/quota",
    "headings": {
      "zh-CN": "配额",
      "zh-TW": "配額",
      "en-US": "Quotas",
      "ja-JP": "クォータ",
      "ko-KR": "할당량",
      "de-DE": "Kontingente",
      "fr-FR": "Quotas",
      "es-ES": "Cuotas"
    }
  },
  {
    "path": "/console/usage",
    "headings": {
      "zh-CN": "用量",
      "zh-TW": "用量",
      "en-US": "Usage",
      "ja-JP": "使用量",
      "ko-KR": "사용량",
      "de-DE": "Nutzung",
      "fr-FR": "Utilisation",
      "es-ES": "Uso"
    }
  },
  {
    "path": "/console/models",
    "headings": {
      "zh-CN": "Agent AI 服务与模型",
      "zh-TW": "Agent AI 服務與模型",
      "en-US": "Agent AI services & models",
      "ja-JP": "Agent の AI サービスとモデル",
      "ko-KR": "Agent AI 서비스 및 모델",
      "de-DE": "KI-Dienste und Modelle der Agents",
      "fr-FR": "Services et modèles d’IA des Agents",
      "es-ES": "Servicios y modelos de IA de Agent"
    }
  },
  {
    "path": "/console/workflows",
    "headings": {
      "zh-CN": "工作流",
      "zh-TW": "工作流程",
      "en-US": "Workflows",
      "ja-JP": "ワークフロー",
      "ko-KR": "워크플로",
      "de-DE": "Workflows",
      "fr-FR": "Workflows",
      "es-ES": "Flujos de trabajo"
    }
  },
  {
    "path": "/console/jobs",
    "headings": {
      "zh-CN": "工作流运行",
      "zh-TW": "工作流程執行",
      "en-US": "Workflow runs",
      "ja-JP": "ワークフローの実行",
      "ko-KR": "워크플로 실행",
      "de-DE": "Workflow-Ausführungen",
      "fr-FR": "Exécutions de workflows",
      "es-ES": "Ejecuciones de flujos de trabajo"
    }
  },
  {
    "path": "/console/jobs/history",
    "headings": {
      "zh-CN": "运行历史",
      "zh-TW": "執行歷史",
      "en-US": "Run history",
      "ja-JP": "実行履歴",
      "ko-KR": "실행 내역",
      "de-DE": "Ausführungsverlauf",
      "fr-FR": "Historique des exécutions",
      "es-ES": "Historial de ejecuciones"
    }
  },
  {
    "path": "/console/jobs/history/job_p10",
    "headings": {
      "zh-CN": "job_p10",
      "zh-TW": "job_p10",
      "en-US": "job_p10",
      "ja-JP": "job_p10",
      "ko-KR": "job_p10",
      "de-DE": "job_p10",
      "fr-FR": "job_p10",
      "es-ES": "job_p10"
    }
  },
  {
    "path": "/console/settings",
    "headings": {
      "zh-CN": "设置",
      "zh-TW": "設定",
      "en-US": "Settings",
      "ja-JP": "設定",
      "ko-KR": "설정",
      "de-DE": "Einstellungen",
      "fr-FR": "Paramètres",
      "es-ES": "Configuración"
    }
  },
  {
    "path": "/console/account",
    "headings": {
      "zh-CN": "账户安全",
      "zh-TW": "帳號安全",
      "en-US": "Account security",
      "ja-JP": "アカウントのセキュリティ",
      "ko-KR": "계정 보안",
      "de-DE": "Kontosicherheit",
      "fr-FR": "Sécurité du compte",
      "es-ES": "Seguridad de la cuenta"
    }
  },
  {
    "path": "/console/fleet",
    "headings": null
  }
] as const;
export const saveText = {
  "zh-CN": "保存",
  "zh-TW": "儲存",
  "en-US": "Save",
  "ja-JP": "保存",
  "ko-KR": "저장",
  "de-DE": "Speichern",
  "fr-FR": "Enregistrer",
  "es-ES": "Guardar"
} as const;
