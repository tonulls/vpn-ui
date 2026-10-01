class AllSetting {

    constructor(data) {
        this.webListen = "";
        this.webDomain = "";
        this.webPort = 2053;
        this.webCertFile = "";
        this.webKeyFile = "";
        this.webBasePath = "/";
        this.sessionMaxAge = 360;
        this.pageSize = 25;
        this.expireDiff = 0;
        this.trafficDiff = 0;
        this.remarkModel = "-ieo";
        this.datepicker = "gregorian";
        this.tgBotEnable = false;
        this.tgBotToken = "";
        this.tgBotProxy = "";
        this.tgBotAPIServer = "";
        this.tgBotChatId = "";
        this.tgBotAdditionalChatId = "";
        this.tgRunTime = "@daily";
        this.tgLang = "en-US";
        this.tgForumEnable = false;
        this.tgForumChatId = "";
        this.tgNotifyDirect = true;
        this.tgNotifyForum = false;
        this.tgNotifyLoginSuccess = true;
        this.tgNotifyLoginFailure = true;
        this.tgNotifyCPU = false;
        this.tgCpu = 80;
        this.tgTopicLoginSuccess = "";
        this.tgTopicLoginFailure = "";
        this.tgTopicCPU = "";
        this.tgBackupEnable = false;
        this.tgBackupIntervalHours = 24;
        this.tgBackupTopicId = "";
        this.tgBackupEncrypt = false;
        this.tgBackupPassword = "";
        this.twoFactorEnable = false;
        this.twoFactorToken = "";
        this.xrayTemplateConfig = "";
        this.subEnable = true;
        this.subJsonEnable = false;
        this.subTitle = "";
        this.subPageTitle = "";
        this.subFaviconUrl = "";
        this.subShowSupport = false;
        this.subSupportUrl = "";
        this.subSupportButtonLabel = "";
        this.subShowProfileUrl = false;
        this.subProfileButtonLabel = "";
        this.subProfileUrl = "";
        this.subAnnounce = "";
        this.subEnableRouting = true;
        this.subRoutingRules = "";
        this.subListen = "";
        this.subPort = 2096;
        this.subPath = "/sub/";
        this.subJsonPath = "/json/";
        this.subClashEnable = true;
        this.subClashPath = "/clash/";
        this.subDomain = "";
        this.externalTrafficInformEnable = false;
        this.externalTrafficInformURI = "";
        this.subCertFile = "";
        this.subKeyFile = "";
        this.subUpdates = 12;
        this.subEncrypt = true;
        this.subShowInfo = true;
        this.subURI = "";
        this.subJsonURI = "";
        this.subClashURI = "";
        this.subJsonFragment = "";
        this.subJsonNoises = "";
        this.subJsonMux = "";
        this.subJsonRules = "";

        this.timeLocation = "Local";

        // LDAP settings
        this.ldapEnable = false;
        this.ldapHost = "";
        this.ldapPort = 389;
        this.ldapUseTLS = false;
        this.ldapBindDN = "";
        this.ldapPassword = "";
        this.ldapBaseDN = "";
        this.ldapUserFilter = "(objectClass=person)";
        this.ldapUserAttr = "mail";
        this.ldapVlessField = "vless_enabled";
        this.ldapSyncCron = "@every 1m";
        this.ldapFlagField = "";
        this.ldapTruthyValues = "true,1,yes,on";
        this.ldapInvertFlag = false;
        this.ldapInboundTags = "";
        this.ldapAutoCreate = false;
        this.ldapAutoDelete = false;
        this.ldapDefaultTotalGB = 0;
        this.ldapDefaultExpiryDays = 0;
        this.ldapDefaultLimitIP = 0;

        if (data == null) {
            return
        }
        ObjectUtil.cloneProps(this, data);
    }

    equals(other) {
        return ObjectUtil.equals(this, other);
    }
}