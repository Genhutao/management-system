package com.xgh.app.ui

import android.content.Context
import android.content.res.Configuration
import android.os.Bundle
import androidx.appcompat.app.AppCompatActivity
import com.xgh.app.data.AppPrefs

/**
 * 全部 Activity 的基类：按设置的字号缩放包裹 Context；
 * 设置里改完字号后，返回到旧页面时检测到缩放不一致自动重建。
 */
abstract class BaseActivity : AppCompatActivity() {

    private var attachedFontScale = 1f

    override fun attachBaseContext(newBase: Context) {
        attachedFontScale = AppPrefs.fontScale
        val config = Configuration(newBase.resources.configuration)
        config.fontScale = attachedFontScale
        super.attachBaseContext(newBase.createConfigurationContext(config))
    }

    override fun onResume() {
        super.onResume()
        if (AppPrefs.fontScale != attachedFontScale) recreate()
    }
}
