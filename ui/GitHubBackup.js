Ext.namespace("com.evait.githubbackup.Utils");

Ext.define("com.evait.githubbackup.Application", {
    extend: "SYNO.SDS.AppInstance",
    appWindowName: "com.evait.githubbackup.MainWindow",
    constructor: function () {
        this.callParent(arguments);
    }
});

Ext.define("com.evait.githubbackup.MainWindow", {
    extend: "SYNO.SDS.AppWindow",
    constructor: function (a) {
        this.appInstance = a.appInstance;
        com.evait.githubbackup.MainWindow.superclass.constructor.call(this, Ext.apply({
            layout: "fit",
            resizable: true,
            cls: "syno-app-win",
            maximizable: true,
            minimizable: true,
            width: 1120,
            height: 820,
            html: com.evait.githubbackup.Utils.getMainHtml()
        }, a));
        com.evait.githubbackup.Utils.ApplicationWindow = this;
    },
    onOpen: function () {
        com.evait.githubbackup.MainWindow.superclass.onOpen.apply(this, arguments);
    },
    onRequest: function (a) {
        com.evait.githubbackup.MainWindow.superclass.onRequest.call(this, a);
    },
    onClose: function () {
        com.evait.githubbackup.MainWindow.superclass.onClose.apply(this, arguments);
        this.doClose();
        return true;
    }
});

Ext.apply(com.evait.githubbackup.Utils, function () {
    return {
        getMainHtml: function () {
            var token = "";
            try {
                if (typeof _S === "function") {
                    token = _S("SynoToken") || "";
                }
                if (!token && window.SYNO && SYNO.SDS && SYNO.SDS.Session) {
                    token = SYNO.SDS.Session.SynoToken || "";
                }
            } catch (e) {
                token = "";
            }
            var query = "?timestamp=" + new Date().getTime();
            if (token) {
                query += "&SynoToken=" + encodeURIComponent(token);
            }
            return '<iframe src="/webman/3rdparty/github-backup/index.html' + query +
                '" title="GitHub Backup" style="width:100%;height:100%;border:0;display:block"></iframe>';
        }
    };
}());
